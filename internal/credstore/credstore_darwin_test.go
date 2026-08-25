//go:build darwin

package credstore

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestClassifyFindError pins down the three stderr shapes find-generic-password
// actually produces (verified against the real /usr/bin/security), so a miss, a
// busy keychain, and any other failure are never confused for one another.
func TestClassifyFindError(t *testing.T) {
	tests := []struct {
		name      string
		stderr    string
		wantMiss  bool
		wantErr   error // sentinel to check with errors.Is; nil means "any non-nil, non-sentinel error"
		wantNoErr bool
	}{
		{
			name:      "item not found is a clean miss",
			stderr:    "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n",
			wantMiss:  true,
			wantNoErr: true,
		},
		{
			name:     "interaction not allowed is busy, not a miss",
			stderr:   "security: SecKeychainItemCopyContent: User interaction is not allowed.\n",
			wantMiss: false,
			wantErr:  ErrBusy,
		},
		{
			name:     "anything else is a real, surfaced error",
			stderr:   "security: SecKeychainOpen: The specified keychain could not be found.\n",
			wantMiss: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			miss, err := classifyFindError([]byte(tt.stderr))
			if miss != tt.wantMiss {
				t.Errorf("miss = %v, want %v", miss, tt.wantMiss)
			}
			switch {
			case tt.wantNoErr:
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				if err == nil {
					t.Error("err = nil, want a surfaced error")
				}
			}
		})
	}
}

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
