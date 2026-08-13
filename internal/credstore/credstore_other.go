//go:build !darwin && !windows

package credstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// fileStore is the fallback Backend for Linux and any other non-darwin,
// non-windows OS. It has no OS keyring dependency (no libsecret), so the secret
// is protected ONLY by filesystem permissions: a single 0600 file in a 0700
// directory under the user's config dir (~/.config/openfortitray/session). That
// is the documented, accepted at-rest boundary on these platforms — a root user
// or anything that can read the user's home can read the cookie, exactly as it
// could read an ssh key.
type fileStore struct {
	mu   sync.Mutex
	path string
}

func newBackend() Backend {
	base, err := os.UserConfigDir()
	if err != nil {
		// Fall back to the working directory only if the OS cannot name a config
		// dir; still 0600 there. In practice UserConfigDir is always resolvable.
		base = "."
	}
	return &fileStore{path: filepath.Join(base, "openfortitray", "session")}
}

// load reads the on-disk key→secret map. A missing file is an empty map, not an
// error, so a first-ever Get is a clean miss.
func (f *fileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		// A corrupt store is treated as empty rather than wedging auth forever;
		// the next Set rewrites it cleanly.
		return map[string]string{}, nil
	}
	return m, nil
}

// save writes the map as 0600 in a 0700 directory, replacing the file.
func (f *fileStore) save(m map[string]string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		return err
	}
	// WriteFile's perm argument applies only when it CREATES the file: writing over
	// an existing file leaves that file's mode alone. Since file permissions are
	// the only thing protecting this secret, assert 0600 (and 0700 on the dir)
	// every time rather than trusting whatever mode a pre-existing file — or a
	// pre-existing config dir — happens to carry.
	if err := os.Chmod(f.path, 0o600); err != nil {
		return err
	}
	// The directory is ours alone (<config>/openfortitray), so tightening it is
	// always safe; best-effort because a filesystem that cannot chmod should not
	// fail an otherwise-good write.
	_ = os.Chmod(dir, 0o700)
	return nil
}

func (f *fileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	return m[key], nil
}

func (f *fileStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

func (f *fileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return f.save(m)
}
