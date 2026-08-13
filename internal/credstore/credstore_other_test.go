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

// os.WriteFile's perm argument applies only when it creates the file, so a store
// file that already exists with wider permissions would keep them and leave the
// cookie readable by everyone. save must re-assert 0600 (and 0700 on its dir) on
// every write, since on this platform permissions are the ONLY protection.
func TestFileStoreTightensExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &fileStore{path: path}
	if err := f.Set("openfortitray:g", "COOKIE"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("pre-existing 0644 store file left at mode %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("pre-existing 0755 store dir left at mode %o, want 700", perm)
	}
}
