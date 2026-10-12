//go:build !windows

package tray

// ForegroundWindow is Windows-only; elsewhere the window hides on the
// frontend's blur event instead. ok is always false.
func ForegroundWindow() (hwnd uintptr, elsewhere bool, ok bool) {
	return 0, false, false
}
