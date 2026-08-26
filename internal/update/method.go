package update

// Method reports how the running build of OpenFortiTray was installed, so a
// later applier task can pick the right upgrade strategy. Detection is a pure,
// side-effect-free inspection: this task performs no process execution and no
// install.
type Method int

const (
	// MethodManual means unknown / drag-installed / Linux — the caller should
	// just open the releases page and let the user update by hand.
	MethodManual Method = iota
	// MethodHomebrew means a macOS Homebrew cask — apply via `brew upgrade --cask`.
	MethodHomebrew
	// MethodWindowsInstaller means Windows — apply by re-running the Setup.exe silently.
	MethodWindowsInstaller
)

// String renders the method for logs and errors.
func (m Method) String() string {
	switch m {
	case MethodHomebrew:
		return "homebrew"
	case MethodWindowsInstaller:
		return "windows-installer"
	default:
		return "manual"
	}
}

// InstallMethod reports how THIS running build was installed. It never errors;
// anything it cannot positively identify reports MethodManual. The
// platform-specific detection lives in method_{darwin,windows,other}.go.
func InstallMethod() Method {
	return installMethod()
}
