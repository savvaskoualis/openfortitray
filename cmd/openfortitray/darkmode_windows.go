//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// isDarkMode reads the same registry value Windows' own Settings ▸
// Personalization ▸ Colors "Choose your mode" control writes:
// AppsUseLightTheme under Themes\Personalize, 0 for dark apps, 1 (or
// missing/unreadable — a fresh account defaults to light) for light. This is
// the same value Explorer, Notepad and every other theme-aware Win32 app
// reads to decide whether to render dark.
func isDarkMode() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}
