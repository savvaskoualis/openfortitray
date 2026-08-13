package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGateway emulates the FortiGate SAML endpoints.
func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/remote/saml/auth_id", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "AUTH123" {
			http.Error(w, "bad id", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: "COOKIEVALUE"})
	})
	return httptest.NewTLSServer(mux)
}

func TestAuthenticateHappyPath(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()

	a := &Authenticator{
		GatewayURL: gw.URL,
		ListenPort: 0, // pick free port; Authenticate must support 0 for tests
		Client:     gw.Client(),
		// Fake browser: immediately performs the redirect the FortiGate would trigger.
		OpenBrowser: nil, // set below, needs the listen address
	}
	a.OpenBrowser = func(loginURL string) error {
		if !strings.HasPrefix(loginURL, gw.URL+"/remote/saml/start?redirect=1") {
			t.Errorf("wrong login URL: %s", loginURL)
		}
		go func() {
			// FortiGate redirects the browser to the local listener with the auth id.
			c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			for i := 0; i < 50; i++ { // wait for listener
				_, err := c.Get(fmt.Sprintf("http://%s/?id=AUTH123", a.listenAddr()))
				if err == nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cookie, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "COOKIEVALUE" {
		t.Fatalf("got cookie %q", cookie)
	}
}

func TestAuthenticateContextCancel(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()
	a := &Authenticator{
		GatewayURL:  gw.URL,
		ListenPort:  0,
		Client:      gw.Client(),
		OpenBrowser: func(string) error { return nil }, // browser never completes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestAuthenticateBadID(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()
	a := &Authenticator{GatewayURL: gw.URL, ListenPort: 0, Client: gw.Client()}
	a.OpenBrowser = func(string) error {
		go func() {
			time.Sleep(50 * time.Millisecond)
			http.Get(fmt.Sprintf("http://%s/?id=WRONG", a.listenAddr()))
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected error for rejected auth id")
	}
}
