package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A redirect must NOT count as a logout. This gateway answers an unauthenticated
// /remote/logout with a 307 to the SAML identity provider, and counting that as
// success made the app log "session ended on the gateway" when nothing had ended.
func TestLogoutRejectsRedirectToIdP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.microsoftonline.com/x/saml2?SAMLRequest=abc",
			http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	err := Logout(context.Background(), srv.Client(), srv.URL, "C")
	if err == nil {
		t.Fatal("a redirect to the IdP must not be reported as a successful logout")
	}
	// The Location carries a SAMLRequest, so it must not be echoed into the log.
	if strings.Contains(err.Error(), "SAMLRequest") {
		t.Errorf("error text leaks the redirect URL: %v", err)
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
