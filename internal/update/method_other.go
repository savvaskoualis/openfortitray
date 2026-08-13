//go:build !darwin && !windows

package update

// installMethod reports MethodManual on every other platform (notably Linux):
// there is no in-app apply strategy, so the caller opens the releases page.
func installMethod() Method {
	return MethodManual
}
