//go:build darwin

package main

import "testing"

// TestOfferBootstrapInstallNilWindowDoesNotPanic guards the fix for the
// Task 2 review finding: main() no longer constructs a.win (nil permanently,
// pending Task 8's Wails rewrite of this dialog), so offerBootstrapInstall
// must return early instead of dereferencing it.
func TestOfferBootstrapInstallNilWindowDoesNotPanic(t *testing.T) {
	a := &app{}
	a.offerBootstrapInstall() // must not panic
}
