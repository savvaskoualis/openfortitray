package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleCask = `cask "openfortitray" do
  version "0.1.20"
  sha256 "a09c470654a4ccc2370f74da8dcef539bb014e8113b0eb68a86c7084b9f8e8aa"

  url "https://example.invalid/v#{version}/OpenFortiTray-v#{version}.dmg"
  name "OpenFortiTray"
end
`

func caskServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Errorf("cask request sent no User-Agent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCaskVersionParsesPinnedVersion(t *testing.T) {
	srv := caskServer(t, http.StatusOK, sampleCask)
	c := CaskChecker{HTTPClient: srv.Client(), URL: srv.URL}
	got, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.1.20" {
		t.Errorf("Version = %q, want 0.1.20", got)
	}
}

func TestCaskVersionErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"non-200", http.StatusNotFound, "nope"},
		{"no version line", http.StatusOK, "cask \"openfortitray\" do\nend\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := caskServer(t, tc.status, tc.body)
			c := CaskChecker{HTTPClient: srv.Client(), URL: srv.URL}
			if _, err := c.Version(context.Background()); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

// The gate's whole purpose: a release the cask has not caught up to must not be
// offered, because `brew upgrade --cask` would no-op on it.
func TestCaskHasTagWithdrawsWhenCaskIsBehind(t *testing.T) {
	srv := caskServer(t, http.StatusOK, sampleCask) // cask at 0.1.20
	c := CaskChecker{HTTPClient: srv.Client(), URL: srv.URL}
	if CaskHasTag(context.Background(), c, "v0.1.21") {
		t.Error("cask at 0.1.20 must not be considered ready for v0.1.21")
	}
}

func TestCaskHasTagAllowsMatchingAndAheadCask(t *testing.T) {
	srv := caskServer(t, http.StatusOK, sampleCask) // cask at 0.1.20
	c := CaskChecker{HTTPClient: srv.Client(), URL: srv.URL}
	// Equal: exactly what a normal, in-lockstep release looks like.
	if !CaskHasTag(context.Background(), c, "v0.1.20") {
		t.Error("cask at 0.1.20 must be considered ready for v0.1.20")
	}
	// Ahead: the tap bumped while our check was in flight — not a reason to hide
	// the update we already found.
	if !CaskHasTag(context.Background(), c, "v0.1.19") {
		t.Error("a cask ahead of the release must still allow the update")
	}
}

// A network/parse failure must fail OPEN: suppressing a real update whenever the
// tap is unreachable would be worse than the no-op this gate prevents.
func TestCaskHasTagFailsOpen(t *testing.T) {
	srv := caskServer(t, http.StatusInternalServerError, "boom")
	c := CaskChecker{HTTPClient: srv.Client(), URL: srv.URL}
	if !CaskHasTag(context.Background(), c, "v0.1.21") {
		t.Error("an unreadable cask must fail open (offer the update anyway)")
	}
}
