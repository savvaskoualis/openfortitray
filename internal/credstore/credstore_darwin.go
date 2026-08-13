//go:build darwin

package credstore

import (
	"errors"
	"os/exec"
	"strings"
)

// securityBin is the macOS keychain CLI. Generic passwords added through it land
// in the user's login keychain, so the secret is scoped to (and unlockable only
// by) the logged-in user — the same trust boundary as the rest of their session.
const securityBin = "/usr/bin/security"

// keychainService is the -s (service) all our items share; the caller's key is
// the -a (account), so one service holds every gateway's cookie.
const keychainService = "openfortitray"

// keychain is the darwin Backend, driving /usr/bin/security. No cgo.
type keychain struct{}

func newBackend() Backend { return keychain{} }

// Get returns the stored password, or ("", nil) when the item is absent.
// find-generic-password -w prints only the password to stdout; a missing item
// exits non-zero (errSecItemNotFound, 44), which we map to a clean miss.
func (keychain) Get(key string) (string, error) {
	out, err := exec.Command(securityBin,
		"find-generic-password", "-s", keychainService, "-a", key, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Any non-zero exit here means "not found" in practice (there is no
			// other failure mode for a well-formed query against the login
			// keychain). Treat it as an empty miss rather than a hard error so a
			// first-ever Connect just falls through to SAML.
			return "", nil
		}
		return "", err
	}
	// -w emits the password followed by a newline.
	return strings.TrimRight(string(out), "\n"), nil
}

// Set stores value under key, replacing any existing item (-U). The value is
// passed via -w on the argv: /usr/bin/security has no option to read a generic
// password from stdin non-interactively (bare -w prompts on a tty, which a
// background app has none of). ACCEPTED EXPOSURE: for the lifetime of this very
// short-lived exec the cookie is visible to `ps` for the same local user (and
// root). This is the local-user boundary the keychain itself assumes; it does
// not widen at-rest exposure (the stored item is user-scoped in the keychain).
func (keychain) Set(key, value string) error {
	// -U updates the item in place if it already exists instead of erroring.
	return exec.Command(securityBin,
		"add-generic-password", "-U",
		"-s", keychainService, "-a", key, "-w", value).Run()
}

// Delete removes the item. A missing item exits non-zero; that is success for an
// idempotent delete.
func (keychain) Delete(key string) error {
	err := exec.Command(securityBin,
		"delete-generic-password", "-s", keychainService, "-a", key).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil // nothing to delete
	}
	return err
}
