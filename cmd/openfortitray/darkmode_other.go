//go:build !darwin && !windows

package main

// isDarkMode always reports false on platforms without a verified
// dark-mode detection path. Linux dark-mode detection (desktop-environment
// specific — GNOME/KDE/etc. each expose it differently) was not implemented
// as part of the Qt migration; see the design doc's known gaps. Windows has
// its own real implementation — see darkmode_windows.go.
func isDarkMode() bool {
	return false
}
