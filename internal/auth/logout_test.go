package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The gateway must receive the session cookie at /remote/logout — that request is
// the whole point: openconnect has no Fortinet logout, so without it the session
// stays established and a one-session-per-user gateway refuses every reconnect
// until it times out.
func TestLogoutSendsCookieToRemoteLogout(t *testing.T) {
	var gotPath, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Logout(context.Background(), srv.Client(), srv.URL, "COOKIEVALUE"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/remote/logout" {
		t.Errorf("path = %q, want /remote/logout", gotPath)
	}
	if gotCookie != "SVPNCOOKIE=COOKIEVALUE" {
		t.Errorf("Cookie header = %q, want SVPNCOOKIE=COOKIEVALUE", gotCookie)
	}
}

// A 302 to the portal is FortiGate's normal answer to a logout; it means the
// session is gone and must not be reported as a failure.
func TestLogoutAcceptsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote/logout" {
			http.Redirect(w, r, "/remote/login", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Logout(context.Background(), srv.Client(), srv.URL, "C"); err != nil {
		t.Errorf("a redirect to the portal must count as logged out, got %v", err)
	}
}

func TestLogoutReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := Logout(context.Background(), srv.Client(), srv.URL, "C"); err == nil {
		t.Error("want an error for a 500, got nil")
	}
}

// Nothing to log out is not an error, and must not produce a request against a
// bogus URL.
func TestLogoutNoopWithoutCookieOrGateway(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	if err := Logout(context.Background(), srv.Client(), srv.URL, ""); err != nil {
		t.Errorf("empty cookie: %v", err)
	}
	if err := Logout(context.Background(), srv.Client(), "", "C"); err != nil {
		t.Errorf("empty gateway: %v", err)
	}
	if called {
		t.Error("no request must be made when there is no session to end")
	}
}
