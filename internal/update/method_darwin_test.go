//go:build darwin

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaskInstalled(t *testing.T) {
	// A prefix pointing at an existing Caskroom/openfortitray-style dir => true.
	present := t.TempDir()
	if err := os.MkdirAll(present, 0o755); err != nil {
		t.Fatalf("mkdir present: %v", err)
	}

	// A prefix that does not exist => false, no panic.
	absent := filepath.Join(t.TempDir(), "does-not-exist")

	tests := []struct {
		name     string
		prefixes []string
		want     bool
	}{
		{"present receipt dir", []string{present}, true},
		{"absent receipt dir", []string{absent}, false},
		{"one of many present", []string{absent, present}, true},
		{"none present", []string{absent, absent}, false},
		{"empty slice", nil, false},
		{"empty string prefix ignored", []string{""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caskInstalled(tt.prefixes); got != tt.want {
				t.Errorf("caskInstalled(%v) = %v, want %v", tt.prefixes, got, tt.want)
			}
		})
	}
}

func TestCaskInstalledRejectsFile(t *testing.T) {
	// A regular file at the prefix path must NOT count as an install.
	f := filepath.Join(t.TempDir(), "openfortitray")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if caskInstalled([]string{f}) {
		t.Error("caskInstalled returned true for a regular file, want false")
	}
}
