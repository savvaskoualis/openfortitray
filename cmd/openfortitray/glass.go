package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// attachGlass attempts to attach this platform's native translucent
// backdrop behind w's content. Best-effort: if w does not implement
// driver.NativeWindow (a driver variant without native-window support,
// e.g. in tests), this is a silent no-op — the window still renders,
// just with today's opaque-minus-the-theme-change look instead of a
// live native blur. Must be called AFTER w.Show(), never before — the
// native window handle RunNative hands back is not guaranteed valid
// until then (confirmed empirically: calling before Show() handed back
// a zero/invalid handle in testing).
func attachGlass(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(attachNativeGlass)
}
