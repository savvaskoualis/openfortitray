package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"dev current never nags", "dev", "v0.1.8", false},
		{"equal versions", "v0.1.8", "v0.1.8", false},
		{"equal versions no v", "0.1.8", "0.1.8", false},
		{"patch bump", "v0.1.7", "v0.1.8", true},
		{"minor bump", "v0.1.9", "v0.2.0", true},
		{"major bump", "v0.9.9", "v1.0.0", true},
		{"downgrade", "v0.2.0", "v0.1.9", false},
		{"leading-v mixed", "0.1.7", "v0.1.8", true},
		{"malformed latest", "v0.1.7", "banana", false},
		// A dev build DOES get offered updates now. A build that can never see an
		// update is a build whose update path is never exercised until a user hits
		// it — and "0.1.7 plus three commits" is genuinely older than 0.1.8.
		{"git-describe current is offered the next release", "v0.1.7-3-gabc123", "v0.1.8", true},
		{"-dev is offered the next release", "0.1.35-dev", "v0.1.36", true},
		// The clean release supersedes a prerelease of the SAME version: this is how
		// a hand-installed 0.1.35-dev moves onto the real 0.1.35.
		{"-dev is offered its own release", "0.1.35-dev", "v0.1.35", true},
		{"release does not supersede itself", "0.1.35", "v0.1.35", false},
		// A dev build of a LATER version is not behind.
		{"-dev ahead of the release", "0.2.0-dev", "v0.1.36", false},
		// Build metadata takes no part in precedence.
		{"build metadata ignored", "0.1.35+abc", "v0.1.35", false},
		{"bare sha current", "abc123", "v0.1.8", false},
		// latest must be CLEAN. Pushing a release candidate at someone who asked for
		// stable is not an update, it is a surprise — however high its version.
		{"prerelease latest is never offered", "v0.1.7", "v0.1.8-rc1", false},
		{"prerelease latest not offered to a dev build either", "0.1.7-dev", "v0.1.8-rc1", false},
		// A bare "dev" or a SHA has no comparable core, so running from source still
		// never nags.
		{"bare dev still never nags", "dev", "v9.9.9", false},
		{"too few components", "v0.1", "v0.2", false},
		{"too many components", "v0.1.7.1", "v0.1.8", false},
		{"empty current", "", "v0.1.8", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Newer(tt.current, tt.latest); got != tt.want {
				t.Errorf("Newer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

const cannedRelease = `{
  "tag_name": "v0.1.8",
  "draft": false,
  "prerelease": false,
  "name": "ignored field",
  "assets": [
    {"name": "OpenFortiTray-v0.1.8.dmg", "browser_download_url": "https://example.test/dl/OpenFortiTray-v0.1.8.dmg", "size": 12345, "extra": "ignore"},
    {"name": "OpenFortiTray-0.1.8-Setup.exe", "browser_download_url": "https://example.test/dl/OpenFortiTray-0.1.8-Setup.exe", "size": 67890},
    {"name": "SHA256SUMS", "browser_download_url": "https://example.test/dl/SHA256SUMS", "size": 128}
  ]
}`

const repo = "savvaskoualis/openfortitray"

func TestLatest(t *testing.T) {
	var gotPath, gotAccept, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cannedRelease))
	}))
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}

	if wantPath := "/repos/" + repo + "/releases/latest"; gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept header = %q, want application/vnd.github+json", gotAccept)
	}
	if gotUA == "" {
		t.Error("User-Agent header was empty, want a default UA")
	}

	if rel.Tag != "v0.1.8" {
		t.Errorf("Tag = %q, want v0.1.8", rel.Tag)
	}
	want := []Asset{
		{Name: "OpenFortiTray-v0.1.8.dmg", URL: "https://example.test/dl/OpenFortiTray-v0.1.8.dmg", Size: 12345},
		{Name: "OpenFortiTray-0.1.8-Setup.exe", URL: "https://example.test/dl/OpenFortiTray-0.1.8-Setup.exe", Size: 67890},
		{Name: "SHA256SUMS", URL: "https://example.test/dl/SHA256SUMS", Size: 128},
	}
	if len(rel.Assets) != len(want) {
		t.Fatalf("got %d assets, want %d", len(rel.Assets), len(want))
	}
	for i, a := range want {
		if rel.Assets[i] != a {
			t.Errorf("asset[%d] = %+v, want %+v", i, rel.Assets[i], a)
		}
	}
}

func TestLatestCustomUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(cannedRelease))
	}))
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo, UserAgent: "custom-ua/1.2"}
	if _, err := c.Latest(context.Background()); err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if gotUA != "custom-ua/1.2" {
		t.Errorf("User-Agent header = %q, want custom-ua/1.2", gotUA)
	}
}

func TestLatestNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("expected error on 403, got nil")
	}
}

func TestAvailable(t *testing.T) {
	newServer := func(tag string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status != http.StatusOK {
				http.Error(w, "boom", status)
				return
			}
			_, _ = w.Write([]byte(`{"tag_name": "` + tag + `", "assets": []}`))
		}))
	}

	t.Run("newer returns release", func(t *testing.T) {
		srv := newServer("v0.1.8", http.StatusOK)
		defer srv.Close()
		c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
		rel, err := c.Available(context.Background(), "v0.1.7")
		if err != nil {
			t.Fatalf("Available error: %v", err)
		}
		if rel == nil {
			t.Fatal("expected a release, got nil")
		}
		if rel.Tag != "v0.1.8" {
			t.Errorf("Tag = %q, want v0.1.8", rel.Tag)
		}
	})

	t.Run("equal returns nil no error", func(t *testing.T) {
		srv := newServer("v0.1.8", http.StatusOK)
		defer srv.Close()
		c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
		rel, err := c.Available(context.Background(), "v0.1.8")
		if err != nil {
			t.Fatalf("Available error: %v", err)
		}
		if rel != nil {
			t.Errorf("expected nil release for equal version, got %+v", rel)
		}
	})

	t.Run("older returns nil no error", func(t *testing.T) {
		srv := newServer("v0.1.7", http.StatusOK)
		defer srv.Close()
		c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
		rel, err := c.Available(context.Background(), "v0.2.0")
		if err != nil {
			t.Fatalf("Available error: %v", err)
		}
		if rel != nil {
			t.Errorf("expected nil release for older version, got %+v", rel)
		}
	})

	t.Run("server 500 surfaces error", func(t *testing.T) {
		srv := newServer("", http.StatusInternalServerError)
		defer srv.Close()
		c := Checker{HTTPClient: srv.Client(), APIBase: srv.URL, Repo: repo}
		if _, err := c.Available(context.Background(), "v0.1.7"); err == nil {
			t.Fatal("expected error on 500, got nil")
		}
	})
}
