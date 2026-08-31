//go:build !darwin

package main

// isDarkMode always reports false on platforms without a verified
// dark-mode detection path. Windows/Linux dark-mode detection was not
// implemented as part of the Qt migration; see the design doc's known
// gaps.
func isDarkMode() bool {
	return false
}
