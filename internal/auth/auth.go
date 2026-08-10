// Package auth implements the Fortinet SAML external-browser login flow.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
)

type Authenticator struct {
	GatewayURL  string
	ListenPort  int
	Client      *http.Client
	OpenBrowser func(url string) error

	mu   sync.Mutex
	addr string // actual listen address once bound (host:port)
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
		client = http.DefaultClient
	}
	openBrowser := a.OpenBrowser
	if openBrowser == nil {
		openBrowser = SystemOpenBrowser
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.ListenPort))
	if err != nil {
		return "", fmt.Errorf("saml listener: %w", err)
	}
	defer ln.Close()
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
			http.Error(w, "login failed, check hyp-vpn logs", http.StatusBadGateway)
			done <- result{err: err}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Hyperio VPN connected — you can close this tab.</h2></body></html>")
		done <- result{cookie: cookie}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	loginURL := a.GatewayURL + "/remote/saml/start?redirect=1"
	if err := openBrowser(loginURL); err != nil {
		return "", fmt.Errorf("open browser: %w", err)
	}

	select {
	case r := <-done:
		return r.cookie, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("saml login not completed: %w", ctx.Err())
	}
}

// exchange trades the browser-delivered auth id for SVPNCOOKIE.
func (a *Authenticator) exchange(ctx context.Context, client *http.Client, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.GatewayURL+"/remote/saml/auth_id?id="+id, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway rejected auth id: %s", resp.Status)
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
