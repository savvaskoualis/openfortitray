//go:build darwin

package credstore

import (
	"os"
	"testing"
	"time"
)

// TestKeychainRoundTrip drives the REAL macOS login keychain. It is gated behind
// availability: a headless/CI runner with no unlocked login keychain (or no
// /usr/bin/security) fails the first Set, and the test skips rather than wedging.
// The key is timestamped and deleted so the test leaves no residue.
func TestKeychainRoundTrip(t *testing.T) {
	if _, err := os.Stat(securityBin); err != nil {
		t.Skipf("%s not present: %v", securityBin, err)
	}
	kc := keychain{}
	key := "openfortitray-test:" + time.Now().Format("20060102150405.000000")
	t.Cleanup(func() { _ = kc.Delete(key) })

	if err := kc.Set(key, "COOKIE-XYZ"); err != nil {
		t.Skipf("keychain unavailable (headless?): %v", err)
	}
	got, err := kc.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != "COOKIE-XYZ" {
		t.Fatalf("Get = %q, want COOKIE-XYZ", got)
	}
	// -U update path: a second Set replaces the value in place.
	if err := kc.Set(key, "COOKIE-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := kc.Get(key); got != "COOKIE-2" {
		t.Fatalf("update: Get = %q, want COOKIE-2", got)
	}
	if err := kc.Delete(key); err != nil {
		t.Fatal(err)
	}
	if got, err := kc.Get(key); err != nil || got != "" {
		t.Fatalf("Get after Delete = (%q, %v), want empty miss", got, err)
	}
	// Idempotent delete of a missing item.
	if err := kc.Delete(key); err != nil {
		t.Fatalf("Delete of missing item errored: %v", err)
	}
}
