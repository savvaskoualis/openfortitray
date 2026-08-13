//go:build !darwin

package main

// installBootstrapHooks is a no-op off macOS. On Linux the privileged helper is
// installed by scripts/install.sh; on Windows the app is already elevated and
// runs openconnect directly. Leaving connectBootstrap/onPermanentError nil makes
// Connect dial directly, exactly as before.
func (a *app) installBootstrapHooks() {}
