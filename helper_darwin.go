//go:build darwin

package openfortitray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Install performs the first-run privileged setup on macOS, prompting once for
// the user's admin password via osascript. Idempotent: if HelperReady already
// reports the helper installed and callable, it does nothing.
func Install() error {
	return installWith(HelperReady, runPrivilegedInstall)
}

// runPrivilegedInstall does the real work. It prepares everything it can as the
// UNPRIVILEGED user first — writing the exact embedded helper to a 0700 temp dir
// — so the only thing the root shell reads from that dir is a file it re-verifies
// against the baked-in sha256. Then it runs the self-contained root installer via
// `osascript ... do shell script "<script>" with administrator privileges`.
//
// SECURITY: see buildRootInstallScript for exactly what is interpolated into the
// privileged string and why it is injection-safe, and escapeAppleScript for how
// the whole script is escaped into the AppleScript literal.
func runPrivilegedInstall() error {
	principal, err := currentPrincipal()
	if err != nil {
		return err
	}
	openconnect, err := resolveOpenconnect()
	if err != nil {
		return err
	}

	workdir, err := os.MkdirTemp("", "openfortitray-bootstrap-")
	if err != nil {
		return fmt.Errorf("openfortitray: cannot create a temp dir: %w", err)
	}
	defer os.RemoveAll(workdir)
	// 0700 dir (MkdirTemp) + 0600 file: the helper source is not readable or
	// writable by other users while it waits for the root read.
	if err := os.WriteFile(filepath.Join(workdir, "openfortitray-tunnel"), []byte(helperScript), 0o600); err != nil {
		return fmt.Errorf("openfortitray: cannot stage the helper: %w", err)
	}

	script, err := buildRootInstallScript(principal, openconnect, workdir)
	if err != nil {
		return err
	}

	// The entire shell script becomes a single AppleScript string literal.
	apple := `do shell script "` + escapeAppleScript(script) + `" with administrator privileges`
	out, err := exec.Command("osascript", "-e", apple).CombinedOutput()
	if err != nil {
		if isUserCancel(string(out)) {
			return ErrUserCancelled
		}
		return fmt.Errorf("openfortitray: privileged install failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
