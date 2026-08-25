package update

import "testing"

func TestMethodString(t *testing.T) {
	tests := []struct {
		m    Method
		want string
	}{
		{MethodManual, "manual"},
		{MethodHomebrew, "homebrew"},
		{MethodWindowsInstaller, "windows-installer"},
		{Method(99), "manual"},
	}
	for _, tt := range tests {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Method(%d).String() = %q, want %q", tt.m, got, tt.want)
		}
	}
}

func TestInstallMethodNeverPanics(t *testing.T) {
	// It must always return one of the known values and never panic,
	// regardless of the host platform.
	switch InstallMethod() {
	case MethodManual, MethodHomebrew, MethodWindowsInstaller:
		// ok
	default:
		t.Fatal("InstallMethod returned an unknown Method")
	}
}
