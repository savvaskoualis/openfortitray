//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A second acquire of a held lock must fail with ErrAlreadyRunning — this is the
// guard that makes main exit instead of starting a second SAML login and connect
// that would fight the first for the one per-user FortiGate session.
func TestInstanceLockSecondAcquireFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfortitray.lock")

	first, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	t.Cleanup(func() { _ = first.release() })

	_, err = acquireInstanceLock(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire err = %v, want ErrAlreadyRunning", err)
	}
	// The message should name the holding pid so the log points at the culprit.
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("error %q should mention the holding pid %d", err, os.Getpid())
	}
}

// A crash leaves the lock file behind naming a now-dead pid, but flock is
// released by the kernel on process death — so the next launch must acquire the
// lock cleanly and overwrite the stale pid. This is what stops a hard crash from
// wedging every future launch.
func TestInstanceLockStaleLockFromDeadPidIsReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfortitray.lock")

	// Pre-seed the file with a pid that is not running (no live process holds a
	// flock on it), mimicking what a crashed instance leaves behind.
	deadPID := unusedPID(t)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", deadPID)), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("acquire over a stale lock failed: %v", err)
	}
	t.Cleanup(func() { _ = lock.release() })

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file = %q, want our pid %d after reclaiming a stale lock", got, os.Getpid())
	}
}

// Releasing the lock must let the same path be acquired again in-process.
func TestInstanceLockReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openfortitray.lock")

	first, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	second, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	_ = second.release()
}

// unusedPID returns a pid that is not a running process, so the stale-lock test
// has a dead pid to seed. kill(pid, 0) probes existence: ESRCH means no such
// process. It searches downward from a high value to avoid the low, likely-live
// pids.
func unusedPID(t *testing.T) int {
	t.Helper()
	for pid := 99999; pid > 2; pid-- {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return pid
		}
	}
	t.Fatal("could not find an unused pid")
	return 0
}
