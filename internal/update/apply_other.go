//go:build !darwin && !windows

package update

import "fmt"

// On platforms without an automatic update strategy (e.g. Linux, where the
// binary is dropped in by hand), both appliers report unsupported so Apply
// returns an error and the caller opens the releases page instead.
func applyHomebrew(pid int) error {
	return fmt.Errorf("update: automatic apply is not supported on this platform")
}

func applyWindows(installerPath string, pid int) error {
	return fmt.Errorf("update: automatic apply is not supported on this platform")
}
