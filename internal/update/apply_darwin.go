//go:build darwin

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// applyHomebrew launches the detached mac updater: wait for the app to exit,
// `brew upgrade --cask openfortitray`, relaunch. brew is resolved from the fixed
// allowlist (never $PATH); if none is present the caller falls back to manual.
func applyHomebrew(pid int) error {
	brew, err := resolveBrewPath()
	if err != nil {
		return err
	}
	script, err := buildBrewScript(pid, brew)
	if err != nil {
		return err
	}
	return spawnDetached("/bin/sh", []string{"-lc", script})
}

// applyWindows is unreachable on darwin (InstallMethod never returns
// MethodWindowsInstaller here); it exists so the package compiles.
func applyWindows(installerPath string, pid int) error {
	return fmt.Errorf("update: windows-installer apply is not available on darwin")
}

// spawnDetached starts name+args in its own process group (Setpgid) so it
// survives the app process exiting, with stdout/stderr appended to the update
// log (best effort). It never waits — the updater must outlive us.
func spawnDetached(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if lp := updateLogPath(); lp != "" {
		_ = os.MkdirAll(filepath.Dir(lp), 0o700)
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update: failed to launch updater: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}
