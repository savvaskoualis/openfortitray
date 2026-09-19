//go:build darwin

package main

import "testing"

// TestOfferBootstrapInstallDoesNotPanicOnBareApp guards offerBootstrapInstall's
// Task 8 Qt-free rewrite: it no longer touches any app field (it only checks
// os.Stat(oft.HelperPath) and calls the package-level tray.ShowMessage, which
// nil-guards itself), so a bare &app{} — as used here, and as main() would
// never actually construct one — must still be safe to call this on.
func TestOfferBootstrapInstallDoesNotPanicOnBareApp(t *testing.T) {
	a := &app{}
	a.offerBootstrapInstall() // must not panic
}
