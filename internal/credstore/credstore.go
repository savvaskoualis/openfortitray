// Package credstore stores small, user-scoped secrets (the VPN session
// SVPNCOOKIE) in platform-native secret storage: the macOS login keychain, a
// Windows DPAPI-encrypted file, or — with no OS keyring dependency — a 0600 file
// on Linux/other. It exists so the tray app can reuse a still-valid session
// cookie across reconnects and restarts instead of driving the SAML browser flow
// every time.
//
// The secret is NEVER written to config.json or the log. Callers namespace their
// keys (the app uses "openfortitray:<gateway>") so different gateways keep
// independent secrets.
//
// The active backend is a package-level seam (see backend / SetBackend) so the
// higher-level cache-first auth flow can be exercised against an in-memory fake
// without touching the real keychain.
package credstore

import "errors"

// ErrBusy indicates the OS secret store could not be reached because it
// currently requires user interaction it cannot provide — e.g. the macOS login
// keychain has not finished its automatic unlock yet at login time. It is
// distinct from a miss (Get returning ("", nil)): the secret may well be
// there, the store just cannot answer yet. Callers may retry shortly.
var ErrBusy = errors.New("credstore: secret store requires interaction")

// Backend persists small secrets keyed by an opaque, caller-namespaced string,
// scoped to the current OS user. Get returns ("", nil) when no secret is stored
// for key — a miss is not an error.
type Backend interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// backend is the process-wide active store, selected per-OS by the build-tagged
// newBackend. Swappable via SetBackend for tests.
var backend Backend = newBackend()

// Get returns the stored secret for key, or ("", nil) if none is stored.
func Get(key string) (string, error) { return backend.Get(key) }

// Set stores value under key, replacing any existing secret.
func Set(key, value string) error { return backend.Set(key, value) }

// Delete removes the secret stored under key. Deleting a missing key is not an
// error.
func Delete(key string) error { return backend.Delete(key) }

// SetBackend installs b as the active store and returns a function that restores
// the previous one. Tests use it to substitute an in-memory fake:
//
//	restore := credstore.SetBackend(NewMemory())
//	defer restore()
func SetBackend(b Backend) (restore func()) {
	prev := backend
	backend = b
	return func() { backend = prev }
}

// Memory is an in-memory Backend for tests. It never touches the OS keychain.
type Memory struct {
	m map[string]string
}

// NewMemory returns an empty in-memory Backend.
func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (b *Memory) Get(key string) (string, error) { return b.m[key], nil }

func (b *Memory) Set(key, value string) error {
	if b.m == nil {
		b.m = map[string]string{}
	}
	b.m[key] = value
	return nil
}

func (b *Memory) Delete(key string) error {
	delete(b.m, key)
	return nil
}
