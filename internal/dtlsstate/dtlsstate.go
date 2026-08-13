// Package dtlsstate remembers which gateways refused a DTLS tunnel, so the app
// stops paying for a DTLS attempt that cannot succeed.
//
// openconnect prefers a DTLS (UDP) tunnel and falls back to HTTPS when it cannot
// get one. On a network that blocks the gateway's UDP port the fallback is not
// free: measured against a real gateway, the config exchange finished in 0.3s,
// openconnect then blocked 5.0s waiting for a DTLS handshake nothing would
// answer, gave up, and repeated the config exchange over HTTPS — a usable tunnel
// at 6.7s instead of ~1s, on every single connect.
//
// The user should not have to know what DTLS is to get a fast connect, so the
// first failure is recorded here and later connects skip DTLS (--no-dtls) for
// that gateway. The record expires after RetryAfter so a network that starts
// permitting UDP is picked back up automatically, and so a one-off failure
// cannot disable DTLS forever.
package dtlsstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RetryAfter is how long a recorded failure suppresses DTLS before it is probed
// again. Long enough that a persistently UDP-blocked network is not re-probed
// daily; short enough that a fixed network recovers DTLS without user action.
const RetryAfter = 7 * 24 * time.Hour

// fileName is the state file inside the config directory. It holds no secrets —
// only gateway names and timestamps — so it needs no special protection beyond
// living in the user's own config directory.
const fileName = "dtls-state.json"

// Store records per-gateway DTLS failures in a small JSON file. It is safe for
// concurrent use; every operation reads and writes the file under the mutex, so
// two goroutines cannot interleave a read-modify-write.
type Store struct {
	mu   sync.Mutex
	path string
}

// New returns a Store backed by dir/dtls-state.json.
func New(dir string) *Store { return &Store{path: filepath.Join(dir, fileName)} }

// state is the on-disk shape: gateway → RFC3339 time of the last DTLS failure.
type state struct {
	Failed map[string]time.Time `json:"failed"`
}

// load reads the state file. Every failure mode — missing, empty, unreadable,
// malformed — yields an empty state rather than an error: the consequence of
// losing this file is one slow connect that records the failure again, so it
// must never be able to block connecting.
func (s *Store) load() state {
	st := state{Failed: map[string]time.Time{}}
	data, err := os.ReadFile(s.path)
	if err != nil || len(data) == 0 {
		return st
	}
	var got state
	if err := json.Unmarshal(data, &got); err != nil || got.Failed == nil {
		return st
	}
	return got
}

// Blocked reports whether DTLS should be skipped for gateway: true when a
// failure was recorded less than RetryAfter ago. A missing or expired record
// returns false, so DTLS is attempted (and, if it works, nothing is recorded).
func (s *Store) Blocked(gateway string, now time.Time) bool {
	if gateway == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.load().Failed[gateway]
	if !ok {
		return false
	}
	// A timestamp in the future (a clock that moved backwards, or a hand-edited
	// file) would otherwise suppress DTLS for longer than RetryAfter.
	if at.After(now) {
		return false
	}
	return now.Sub(at) < RetryAfter
}

// MarkFailed records that gateway refused a DTLS tunnel at now, replacing any
// earlier record (so the suppression window restarts from the latest failure).
// Errors are returned for logging; the caller treats them as non-fatal.
func (s *Store) MarkFailed(gateway string, now time.Time) error {
	if gateway == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	st.Failed[gateway] = now.UTC()
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
