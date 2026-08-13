package credstore

import "testing"

// TestMemoryRoundTrip exercises the in-memory fake: Set→Get→Delete→Get(miss).
func TestMemoryRoundTrip(t *testing.T) {
	b := NewMemory()
	if v, err := b.Get("missing"); err != nil || v != "" {
		t.Fatalf("empty Get = (%q, %v), want (\"\", nil)", v, err)
	}
	if err := b.Set("openfortitray:vpn.example.com", "COOKIE-123"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Get("openfortitray:vpn.example.com"); err != nil || v != "COOKIE-123" {
		t.Fatalf("Get after Set = (%q, %v), want COOKIE-123", v, err)
	}
	if err := b.Delete("openfortitray:vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Get("openfortitray:vpn.example.com"); err != nil || v != "" {
		t.Fatalf("Get after Delete = (%q, %v), want empty", v, err)
	}
	// Deleting a missing key is not an error (idempotent).
	if err := b.Delete("nope"); err != nil {
		t.Fatalf("Delete of missing key errored: %v", err)
	}
}

// TestSetBackendSeam verifies the package-level Get/Set/Delete delegate to the
// swapped-in backend and that the restore func puts the previous one back — the
// seam the higher-level auth flow relies on for hermetic tests.
func TestSetBackendSeam(t *testing.T) {
	fake := NewMemory()
	restore := SetBackend(fake)
	defer restore()

	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got, _ := fake.Get("k"); got != "v" {
		t.Fatalf("Set did not reach the swapped backend: %q", got)
	}
	if got, err := Get("k"); err != nil || got != "v" {
		t.Fatalf("Get = (%q, %v), want v", got, err)
	}
	if err := Delete("k"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get("k"); got != "" {
		t.Fatalf("Delete did not reach the swapped backend: %q", got)
	}

	restore()
	// After restore the swapped fake is no longer the active backend.
	if backend == Backend(fake) {
		t.Fatal("restore did not reinstate the previous backend")
	}
}
