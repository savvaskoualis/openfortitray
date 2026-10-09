//go:build windows

package tray

// platformIcon converts a tray PNG to the ICO the Windows systray requires
// (see pngToICO), falling back to the input if conversion fails — it never
// should, these are our own embedded assets.
func platformIcon(png []byte) []byte {
	if ico, err := pngToICO(png); err == nil {
		return ico
	}
	return png
}
