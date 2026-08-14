package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"testing"
)

func TestParseSHA256SUMS(t *testing.T) {
	// Two-space separator, trailing newline, CRLF line, a *binary marker, blank
	// lines, and a couple of garbage lines that must be skipped.
	body := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  OpenFortiTray-v0.1.8.dmg\n" +
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB  OpenFortiTray-0.1.8-Setup.exe\r\n" +
		"\n" +
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc *binary-mode.bin\n" +
		"not-a-valid-line\n" +
		"deadbeef  too-short-digest\n" +
		"   \n"

	got := parseSHA256SUMS([]byte(body))
	want := map[string]string{
		"OpenFortiTray-v0.1.8.dmg":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"OpenFortiTray-0.1.8-Setup.exe": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"binary-mode.bin":               "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSHA256SUMS mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseSHA256SUMSEmpty(t *testing.T) {
	if got := parseSHA256SUMS(nil); len(got) != 0 {
		t.Errorf("expected empty map, got %#v", got)
	}
}

// sha256Hex returns the lowercase hex digest of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newDownloadFixture spins a TLS test server serving a payload at /asset and a
// SHA256SUMS at /sums. The returned Checker trusts the server's cert. The sums
// body and behaviour are configurable via the returned handler struct fields.
type fixture struct {
	srv      *httptest.Server
	checker  Checker
	assetURL string
	sumsURL  string
}

func newFixture(t *testing.T, payload, sumsBody []byte) *fixture {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sumsBody)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return &fixture{
		srv:      srv,
		checker:  Checker{HTTPClient: srv.Client()},
		assetURL: srv.URL + "/asset",
		sumsURL:  srv.URL + "/sums",
	}
}

func TestDownloadAndVerifyHappyPath(t *testing.T) {
	payload := []byte("this is the OpenFortiTray installer payload, pretend it is 20MB")
	sums := sha256Hex(payload) + "  OpenFortiTray-v0.1.8.dmg\n"
	fx := newFixture(t, payload, []byte(sums))

	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: fx.assetURL, Size: int64(len(payload))}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: fx.sumsURL}

	path, err := fx.checker.DownloadAndVerify(context.Background(), asset, sumsAsset)
	if err != nil {
		t.Fatalf("DownloadAndVerify error: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read verified file: %v", err)
	}
	if !reflect.DeepEqual(got, payload) {
		t.Errorf("verified file bytes do not match payload")
	}
	if sha256Hex(got) != sha256Hex(payload) {
		t.Errorf("digest of verified file does not line up")
	}
	// The file must be private (0600) on POSIX. Windows has no POSIX mode bits —
	// Go reports 0666 for any writable file there and privacy comes from the
	// 0700 parent dir + the user-profile temp root — so skip the bit check.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat verified file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file perm = %o, want 600", perm)
		}
	}
}

func TestDownloadAndVerifyTamperedPayload(t *testing.T) {
	payload := []byte("tampered bytes that do not match the published sum")
	// Publish a digest for entirely different content.
	sums := sha256Hex([]byte("the original honest payload")) + "  OpenFortiTray-v0.1.8.dmg\n"
	fx := newFixture(t, payload, []byte(sums))

	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: fx.assetURL}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: fx.sumsURL}

	path, err := fx.checker.DownloadAndVerify(context.Background(), asset, sumsAsset)
	if err == nil {
		_ = os.RemoveAll(path)
		t.Fatal("expected error on tampered payload, got nil")
	}
	if path != "" {
		t.Errorf("expected empty path on failure, got %q", path)
	}
}

func TestDownloadAndVerifyRemovesPartialOnMismatch(t *testing.T) {
	// Prove the temp file is gone after a mismatch by watching the temp root
	// before/after. We assert no leftover openfortitray-dl-* dir survives.
	payload := []byte("bytes A")
	sums := sha256Hex([]byte("bytes B")) + "  OpenFortiTray-v0.1.8.dmg\n"
	fx := newFixture(t, payload, []byte(sums))

	before := countDLDirs(t)
	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: fx.assetURL}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: fx.sumsURL}
	if _, err := fx.checker.DownloadAndVerify(context.Background(), asset, sumsAsset); err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if after := countDLDirs(t); after != before {
		t.Errorf("temp dir leaked: before=%d after=%d", before, after)
	}
}

func countDLDirs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) >= len("openfortitray-dl-") && e.Name()[:len("openfortitray-dl-")] == "openfortitray-dl-" {
			n++
		}
	}
	return n
}

func TestDownloadAndVerifyAssetNotInSums(t *testing.T) {
	payload := []byte("some payload")
	// SHA256SUMS lists a different filename entirely.
	sums := sha256Hex(payload) + "  some-other-file.dmg\n"
	fx := newFixture(t, payload, []byte(sums))

	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: fx.assetURL}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: fx.sumsURL}

	if _, err := fx.checker.DownloadAndVerify(context.Background(), asset, sumsAsset); err == nil {
		t.Fatal("expected error when asset absent from SHA256SUMS, got nil")
	}
}

func TestDownloadAndVerifyHTTPRedirectRejected(t *testing.T) {
	payload := []byte("payload")
	sums := sha256Hex(payload) + "  OpenFortiTray-v0.1.8.dmg\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	// The asset endpoint 302-redirects to a plain http:// URL, which the
	// CheckRedirect must refuse before ever fetching it.
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/downgraded", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client()}
	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: srv.URL + "/asset"}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: srv.URL + "/sums"}

	if _, err := c.DownloadAndVerify(context.Background(), asset, sumsAsset); err == nil {
		t.Fatal("expected error on http-scheme redirect, got nil")
	}
}

func TestDownloadAndVerifyHTTPSRedirectAllowed(t *testing.T) {
	payload := []byte("payload behind an https redirect")
	sums := sha256Hex(payload) + "  OpenFortiTray-v0.1.8.dmg\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	mux.HandleFunc("/real-asset", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	// Redirect to another https path on the SAME server (same cert, so the
	// client trusts it). This exercises the "https redirect allowed" path.
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/real-asset", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client()}
	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: srv.URL + "/asset"}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: srv.URL + "/sums"}

	path, err := c.DownloadAndVerify(context.Background(), asset, sumsAsset)
	if err != nil {
		t.Fatalf("expected https redirect to be followed, got error: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read verified file: %v", err)
	}
	if !reflect.DeepEqual(got, payload) {
		t.Errorf("bytes behind https redirect do not match")
	}
}

func TestDownloadAndVerifyRejectsNonHTTPSInitialURL(t *testing.T) {
	// Plain http server: the initial scheme check must reject it outright.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client()}
	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: srv.URL + "/asset"}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: srv.URL + "/sums"}
	if _, err := c.DownloadAndVerify(context.Background(), asset, sumsAsset); err == nil {
		t.Fatal("expected error for non-https initial URL, got nil")
	}
}

func TestDownloadAndVerifyContextCancelled(t *testing.T) {
	payload := []byte("prefix-bytes-then-block")
	sums := sha256Hex(payload) + "  OpenFortiTray-v0.1.8.dmg\n"

	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("partial"))
			f.Flush()
		}
		close(started)
		// Block until the client cancels its context (request context fires).
		<-r.Context().Done()
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	c := Checker{HTTPClient: srv.Client()}
	asset := Asset{Name: "OpenFortiTray-v0.1.8.dmg", URL: srv.URL + "/asset"}
	sumsAsset := Asset{Name: "SHA256SUMS", URL: srv.URL + "/sums"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	defer cancel()

	if _, err := c.DownloadAndVerify(ctx, asset, sumsAsset); err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}
