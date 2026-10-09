//go:build !windows

package tray

// platformIcon is the identity outside Windows: macOS and Linux trays take PNG.
func platformIcon(png []byte) []byte { return png }
