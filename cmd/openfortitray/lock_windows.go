//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrAlreadyRunning is returned by acquireInstanceLock when another live instance
// already holds the lock. main turns it into a clean exit rather than a second
// SAML login + connect.
var ErrAlreadyRunning = errors.New("another openfortitray instance is already running")

// instanceLock holds the process-wide single-instance lock. Windows has no
// flock(2), so this is a pidfile: the file exists while an instance runs, and a
// stale one left by a crash is reclaimed by checking whether the recorded pid is
// still alive (see acquireInstanceLock).
type instanceLock struct {
	path string
	f    *os.File
}

// acquireInstanceLock creates path exclusively so only one instance runs. If the
// file already exists it reads the recorded pid: a live pid means another
// instance is running (ErrAlreadyRunning); a dead pid means a previous instance
// crashed without cleaning up, so the stale file is removed and the lock retaken.
func acquireInstanceLock(path string) (*instanceLock, error) {
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Sync()
			return &instanceLock{path: path, f: f}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("open lock file %s: %w", path, err)
		}
		pid := readLockPIDFromPath(path)
		if pid > 0 && pidAlive(pid) {
			return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, pid)
		}
		// Stale lock from a dead pid: reclaim it and try again.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("clear stale lock %s: %w", path, err)
		}
	}
}

// readLockPIDFromPath reads the pid recorded in a lock file. Returns 0 if it
// cannot be read or parsed.
func readLockPIDFromPath(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// pidAlive reports whether a process with the given pid currently exists. On
// Windows os.FindProcess opens the process handle and fails for a pid that is not
// running, which is exactly the liveness signal we need.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

// release removes the pidfile and closes it. Safe on a nil lock.
func (l *instanceLock) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	_ = os.Remove(l.path)
	return err
}
