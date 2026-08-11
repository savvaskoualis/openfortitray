package autostart

import "os/exec"

// taskName must match the name used by the Windows install script.
const taskName = "Postern"

// Enable registers an ONLOGON scheduled task running exePath.
//
// The /TR value is wrapped in literal double quotes: schtasks stores the action
// string as given, and Task Scheduler splits it on whitespace at run time, so an
// unquoted path under %ProgramFiles% would create fine but fail to launch.
func Enable(exePath string) error {
	return exec.Command("schtasks", "/Create", "/TN", taskName,
		"/SC", "ONLOGON", "/RL", "HIGHEST", "/TR", `"`+exePath+`"`, "/F").Run()
}

// Disable deletes the scheduled task.
func Disable() error {
	return exec.Command("schtasks", "/Delete", "/TN", taskName, "/F").Run()
}

// IsEnabled reports whether the scheduled task exists.
func IsEnabled() bool {
	return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}
