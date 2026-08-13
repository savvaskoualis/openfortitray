package dtlsstate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarkFailedThenBlocked(t *testing.T) {
	s := New(t.TempDir())
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

	if s.Blocked("gw:10443", now) {
		t.Fatal("a gateway with no record must not be blocked")
	}
	if err := s.MarkFailed("gw:10443", now); err != nil {
		t.Fatal(err)
	}
	if !s.Blocked("gw:10443", now.Add(time.Hour)) {
		t.Error("a fresh failure must suppress DTLS")
	}
	// A different gateway is unaffected: DTLS may work there.
	if s.Blocked("other:10443", now.Add(time.Hour)) {
		t.Error("one gateway's failure must not suppress DTLS on another")
	}
}

// The record must expire, so a network that starts permitting UDP recovers DTLS
// without the user doing anything.
func TestRecordExpires(t *testing.T) {
	s := New(t.TempDir())
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if err := s.MarkFailed("gw:10443", now); err != nil {
		t.Fatal(err)
	}
	if !s.Blocked("gw:10443", now.Add(RetryAfter-time.Minute)) {
		t.Error("must still be blocked just inside the window")
	}
	if s.Blocked("gw:10443", now.Add(RetryAfter+time.Minute)) {
		t.Error("must be probed again once the window has passed")
	}
}

// A later failure restarts the window rather than leaving the original expiry.
func TestMarkFailedRestartsWindow(t *testing.T) {
	s := New(t.TempDir())
	t0 := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if err := s.MarkFailed("gw:10443", t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(RetryAfter - time.Hour)
	if err := s.MarkFailed("gw:10443", t1); err != nil {
		t.Fatal(err)
	}
	if !s.Blocked("gw:10443", t0.Add(RetryAfter+time.Hour)) {
		t.Error("the second failure must restart the suppression window")
	}
}

// Losing or corrupting the file costs one slow connect, never a failure to
// connect — so every unreadable state must behave as "no record".
func TestUnreadableStateFailsOpen(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Blocked("gw:10443", time.Now()) {
		t.Error("a corrupt state file must not suppress DTLS")
	}
	// And it must still be writable over the corrupt content.
	if err := s.MarkFailed("gw:10443", time.Now()); err != nil {
		t.Fatalf("MarkFailed over a corrupt file: %v", err)
	}
	if !s.Blocked("gw:10443", time.Now()) {
		t.Error("MarkFailed must have replaced the corrupt state")
	}
}

// A timestamp in the future (clock moved backwards, hand-edited file) must not
// suppress DTLS for longer than RetryAfter.
func TestFutureTimestampIsIgnored(t *testing.T) {
	s := New(t.TempDir())
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if err := s.MarkFailed("gw:10443", now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Blocked("gw:10443", now) {
		t.Error("a future-dated record must be ignored")
	}
}

func TestEmptyGatewayIsNoop(t *testing.T) {
	s := New(t.TempDir())
	if err := s.MarkFailed("", time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Blocked("", time.Now()) {
		t.Error(`the empty gateway must never be reported blocked`)
	}
}
