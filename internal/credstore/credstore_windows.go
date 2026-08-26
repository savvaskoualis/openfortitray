//go:build windows

package credstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiStore is the Windows Backend. It encrypts the key→secret map with DPAPI
// (CryptProtectData at USER scope — CRYPTPROTECT_LOCAL_MACHINE is off) and keeps
// the ciphertext in a 0600 file under %APPDATA%\openfortitray\session.bin. Only
// the same Windows user can CryptUnprotectData it, so the cookie is bound to the
// logged-in user account, not merely to file permissions.
type dpapiStore struct {
	mu   sync.Mutex
	path string
}

func newBackend() Backend {
	base, err := os.UserConfigDir() // %AppData% (Roaming) on Windows
	if err != nil {
		base = "."
	}
	return &dpapiStore{path: filepath.Join(base, "openfortitray", "session.bin")}
}

// dataBlob mirrors Win32 DATA_BLOB (a length + pointer pair) for the crypt32
// calls below.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) dataBlob {
	if len(d) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

var (
	crypt32       = windows.NewLazySystemDLL("crypt32.dll")
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

// protect encrypts data for the current user (dwFlags 0 → user scope, not
// machine scope).
func protect(data []byte) ([]byte, error) {
	in := newBlob(data)
	var out dataBlob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // szDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags: user scope
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// unprotect decrypts data previously produced by protect for this user.
func unprotect(data []byte) ([]byte, error) {
	in := newBlob(data)
	var out dataBlob
	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // ppszDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// load reads and decrypts the key→secret map. A missing file is an empty map.
func (d *dpapiStore) load() (map[string]string, error) {
	enc, err := os.ReadFile(d.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return map[string]string{}, nil
	}
	plain, err := unprotect(enc)
	if err != nil {
		// Ciphertext this user cannot decrypt (copied from another account, or
		// corrupt) is treated as empty; the next Set rewrites it.
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(plain, &m); err != nil {
		return map[string]string{}, nil
	}
	return m, nil
}

// save encrypts and writes the map as a 0600 file in a 0700 directory.
func (d *dpapiStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	enc, err := protect(plain)
	if err != nil {
		return err
	}
	return os.WriteFile(d.path, enc, 0o600)
}

func (d *dpapiStore) Get(key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, err := d.load()
	if err != nil {
		return "", err
	}
	return m[key], nil
}

func (d *dpapiStore) Set(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, err := d.load()
	if err != nil {
		return err
	}
	m[key] = value
	return d.save(m)
}

func (d *dpapiStore) Delete(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, err := d.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return d.save(m)
}
