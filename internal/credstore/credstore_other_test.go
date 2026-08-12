//go:build !darwin && !windows

package credstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileStoreRoundTrip drives the real Linux/other backend against a temp path
// (hermetic: it never touches the user's real config dir) and asserts the file
// is created 0600.
func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := &fileStore{path: filepath.Join(dir, "sub", "session")}

	if v, err := f.Get("openfortitray:g"); err != nil || v != "" {
		t.Fatalf("empty Get = (%q, %v), want empty", v, err)
	}
	if err := f.Set("openfortitray:g", "COOKIE"); err != nil {
		t.Fatal(err)
	}
	// Two independent keys must not clobber each other.
	if err := f.Set("openfortitray:h", "OTHER"); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Get("openfortitray:g"); err != nil || v != "COOKIE" {
		t.Fatalf("Get = (%q, %v), want COOKIE", v, err)
	}
	if v, _ := f.Get("openfortitray:h"); v != "OTHER" {
		t.Fatalf("second key lost: %q", v)
	}

	info, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file mode = %o, want 600", perm)
	}

	if err := f.Delete("openfortitray:g"); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("openfortitray:g"); v != "" {
		t.Fatalf("Get after Delete = %q, want empty", v)
	}
	if v, _ := f.Get("openfortitray:h"); v != "OTHER" {
		t.Fatalf("Delete removed the wrong key: %q", v)
	}
	// Idempotent delete of a missing key.
	if err := f.Delete("openfortitray:g"); err != nil {
		t.Fatalf("Delete of missing key errored: %v", err)
	}
}
