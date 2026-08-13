//go:build windows

package credstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDPAPIStoreRoundTrip drives the real Windows DPAPI backend against a temp
// path (hermetic). DPAPI needs a real user context, which CI runners have; if
// CryptProtectData is somehow unavailable the first Set errors and the test
// skips rather than failing spuriously.
func TestDPAPIStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := &dpapiStore{path: filepath.Join(dir, "sub", "session.bin")}

	if v, err := d.Get("openfortitray:g"); err != nil || v != "" {
		t.Fatalf("empty Get = (%q, %v), want empty", v, err)
	}
	if err := d.Set("openfortitray:g", "COOKIE"); err != nil {
		t.Skipf("DPAPI unavailable: %v", err)
	}
	if err := d.Set("openfortitray:h", "OTHER"); err != nil {
		t.Fatal(err)
	}

	// The on-disk bytes must be ciphertext, not the plaintext cookie.
	enc, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 {
		t.Fatal("session.bin is empty")
	}
	for i := 0; i+6 <= len(enc); i++ {
		if string(enc[i:i+6]) == "COOKIE" {
			t.Fatal("plaintext cookie found in session.bin — DPAPI did not encrypt")
		}
	}

	if v, err := d.Get("openfortitray:g"); err != nil || v != "COOKIE" {
		t.Fatalf("Get = (%q, %v), want COOKIE", v, err)
	}
	if v, _ := d.Get("openfortitray:h"); v != "OTHER" {
		t.Fatalf("second key lost: %q", v)
	}
	if err := d.Delete("openfortitray:g"); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get("openfortitray:g"); v != "" {
		t.Fatalf("Get after Delete = %q, want empty", v)
	}
	if v, _ := d.Get("openfortitray:h"); v != "OTHER" {
		t.Fatalf("Delete removed the wrong key: %q", v)
	}
}
