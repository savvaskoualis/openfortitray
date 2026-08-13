//go:build windows

package update

// installMethod reports MethodWindowsInstaller: Windows builds are shipped via
// a Setup.exe, so the applier re-runs it silently to upgrade.
func installMethod() Method {
	return MethodWindowsInstaller
}
