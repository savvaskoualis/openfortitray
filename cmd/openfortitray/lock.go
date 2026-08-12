//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ErrAlreadyRunning is returned by acquireInstanceLock when another live instance
// already holds the lock. main turns it into a clean exit rather than a second
// SAML login + connect, which is what let two instances fight over the one
// per-user FortiGate session and storm the gateway with cookie-rejected retries.
var ErrAlreadyRunning = errors.New("another openfortitray instance is already running")

// instanceLock holds the process-wide single-instance advisory lock. Keep it for
// the lifetime of the process; release() (or process death) frees it.
type instanceLock struct {
	f *os.File
}

// acquireInstanceLock takes an exclusive, non-blocking advisory lock on path so
// only one instance of the app runs at a time.
//
// flock(2) is the guard, not the file's contents: the lock lives on the open
// file description and the KERNEL RELEASES IT WHEN THE PROCESS DIES, so a crash
// (or SIGKILL, or power loss) leaves no stale lock to clear — the next launch
// acquires it cleanly even though the file still names the dead pid. The pid we
// write is purely informational, for the "already running (pid N)" log line.
//
// Returns ErrAlreadyRunning (wrapped) if a live instance holds the lock.
func acquireInstanceLock(path string) (*instanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// EWOULDBLOCK/EAGAIN means a live instance holds the lock. Anything else is
		// a real error (bad path, unsupported filesystem) the caller should see.
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if pid := readLockPID(f); pid != "" {
				return nil, fmt.Errorf("%w (pid %s)", ErrAlreadyRunning, pid)
			}
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// We hold the lock. Record our pid for diagnostics; failures here are not
	// fatal because flock, not the file body, is what enforces exclusion.
	if err := f.Truncate(0); err == nil {
		if _, err := f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0); err == nil {
			_ = f.Sync()
		}
	}
	return &instanceLock{f: f}, nil
}

// readLockPID best-effort reads the pid recorded in an already-held lock file,
// for the "already running" message. Returns "" if it cannot be read.
func readLockPID(f *os.File) string {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	return strings.TrimSpace(string(buf[:n]))
}

// release drops the lock and closes the file. Safe on a nil lock. flock also
// releases automatically on process exit, so this is a courtesy for the
// in-process case (and for tests).
func (l *instanceLock) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
