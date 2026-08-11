// Package auth implements the Fortinet SAML external-browser login flow.
package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// DefaultUserAgent is sent on the auth_id exchange. FortiGate SSL-VPN gateways
// reject unrecognized clients (Go's default "Go-http-client/1.1" often draws a
// 403), so present a FortiClient-like agent matching the browser that just
// completed the SAML login.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X) FortiClient Postern"

type Authenticator struct {
	GatewayURL  string
	ListenPort  int
	Client      *http.Client
	UserAgent   string // exchange User-Agent; empty → DefaultUserAgent
	OpenBrowser func(url string) error

	mu   sync.Mutex
	addr string // actual listen address once bound (host:port)
}

// http1Client is the fallback client used when none is injected. FortiGate's
// SSL-VPN port frequently only speaks HTTP/1.1 and 403s an HTTP/2 request, so
// HTTP/2 is disabled by giving an empty TLSNextProto map.
func http1Client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		},
	}
}

func (a *Authenticator) listenAddr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

// Authenticate runs the SAML flow and returns the SVPNCOOKIE value.
func (a *Authenticator) Authenticate(ctx context.Context) (string, error) {
	client := a.Client
	if client == nil {
		client = http1Client()
	}
	openBrowser := a.OpenBrowser
	if openBrowser == nil {
		openBrowser = SystemOpenBrowser
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.ListenPort))
	if err != nil {
		return "", fmt.Errorf("saml listener: %w", err)
	}
	a.mu.Lock()
	a.addr = ln.Addr().String()
	a.mu.Unlock()

	type result struct {
		cookie string
		err    error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		cookie, err := a.exchange(ctx, client, id)
		if err != nil {
			http.Error(w, "login failed, check postern logs", http.StatusBadGateway)
			select {
			case done <- result{err: err}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><html><head><meta charset=\"utf-8\"></head>"+
			"<body><h2>Postern connected — you can close this tab.</h2></body></html>")
		select {
		case done <- result{cookie: cookie}:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	loginURL := a.GatewayURL + "/remote/saml/start?redirect=1"
	if err := openBrowser(loginURL); err != nil {
		srv.Close()
		return "", fmt.Errorf("open browser: %w", err)
	}

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		srv.Shutdown(context.Background())
		return "", fmt.Errorf("saml login not completed: %w", ctx.Err())
	}

	srv.Shutdown(context.Background())
	return res.cookie, res.err
}

// exchange trades the browser-delivered auth id for SVPNCOOKIE.
func (a *Authenticator) exchange(ctx context.Context, client *http.Client, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.GatewayURL+"/remote/saml/auth_id?id="+url.QueryEscape(id), nil)
	if err != nil {
		return "", err
	}
	ua := a.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// FortiGate states the refusal reason in the body; surface a snippet so
		// the log line identifies the cause (host-check, realm, UA, expired id).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		snippet := strings.TrimSpace(string(body))
		return "", fmt.Errorf("gateway rejected auth id: %s (proto %s, body: %q)",
			resp.Status, resp.Proto, snippet)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "SVPNCOOKIE" && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", errors.New("no SVPNCOOKIE in gateway response")
}

// SystemOpenBrowser opens url in the OS default browser.
func SystemOpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
