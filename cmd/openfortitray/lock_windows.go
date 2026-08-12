//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// ErrAlreadyRunning is returned by acquireInstanceLock when another live instance
// already holds the lock. main turns it into a clean exit rather than a second
// SAML login + connect.
var ErrAlreadyRunning = errors.New("another openfortitray instance is already running")

// mutexName is the single-instance mutex. It lives in the session-local
// namespace (no "Global\") — every instance runs elevated in the same user
// session, so they share it, while different users/sessions stay independent.
const mutexName = `Local\io.github.savvaskoualis.openfortitray.single-instance`

// instanceLock holds the process-wide single-instance lock as a named mutex —
// the correct Windows primitive. Unlike the old pidfile, the kernel releases the
// mutex automatically when the owning process dies, so a crash never leaves a
// stale lock that blocks every future launch; and there is no pid-reuse hazard
// (os.FindProcess cannot tell a dead pid from a reused one on Windows, which is
// exactly what made the pidfile approach wedge after a crash).
type instanceLock struct {
	h windows.Handle
}

// acquireInstanceLock creates the named mutex. CreateMutex returns a valid handle
// even when the mutex already exists; ERROR_ALREADY_EXISTS is the signal that
// another instance owns it. The path argument is unused on Windows (kept for the
// cross-platform signature). We hold the handle open for the process lifetime;
// release() (or process exit) frees it.
func acquireInstanceLock(_ string) (*instanceLock, error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, fmt.Errorf("single-instance mutex name: %w", err)
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return nil, ErrAlreadyRunning
	}
	if h == 0 {
		return nil, fmt.Errorf("create single-instance mutex: %w", err)
	}
	return &instanceLock{h: h}, nil
}

// release closes the mutex handle, letting the kernel free the named mutex. Safe
// on a nil lock.
func (l *instanceLock) release() error {
	if l == nil || l.h == 0 {
		return nil
	}
	err := windows.CloseHandle(l.h)
	l.h = 0
	return err
}
