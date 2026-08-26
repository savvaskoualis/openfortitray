//go:build !darwin

package openfortitray

import "errors"

// Install is macOS-only. On Linux the privileged helper is installed by
// scripts/install.sh (checkout) or scripts/install-helper.sh (curl|sudo bash);
// on Windows the app is already elevated and runs openconnect directly, so there
// is no helper to bootstrap. This keeps the existing manual path unchanged.
func Install() error {
	return errors.New("openfortitray: automatic helper install is macOS-only; run scripts/install.sh")
}
