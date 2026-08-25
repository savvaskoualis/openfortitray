package update

import (
	"context"
	"fmt"
	"os/exec"
)

// Prepared is the result of Prepare: everything an update needs that can be done
// while the app is still running.
type Prepared struct {
	// InstallerPath is the verified installer, on the Windows path only. The
	// Homebrew path leaves it empty: brew re-resolves and re-verifies the download
	// from its own cache, keyed by the cask's sha.
	InstallerPath string
}

// Prepare does the slow, network-bound half of an update WITHOUT touching the
// installed app, so it can run while the app is alive and visible.
//
// This split is the whole point. The updater has to replace the app's own files,
// so it runs detached and waits for the process to exit — which meant the app
// vanished for the entire download and install, with no way to report progress
// because it was dead for all of it. Downloading first shrinks the dead window to
// the final swap.
//
// On Homebrew that means `brew update` (refresh the tap, which is also what makes a
// custom-tap cask upgradeable at all) followed by `brew fetch`, which downloads and
// caches the new dmg without installing it. The later `brew upgrade` then runs from
// cache and is quick.
//
// On Windows it means downloading and hash-verifying Setup.exe, which the app
// already did — this just names that step as part of a flow rather than something
// buried inside apply.
//
// It is blocking and must not run on the UI goroutine.
func Prepare(ctx context.Context, method Method, dl func(context.Context) (string, error)) (Prepared, error) {
	switch method {
	case MethodHomebrew:
		brew, err := resolveBrewPath()
		if err != nil {
			return Prepared{}, err
		}
		// `brew update` first: the cask lives in a custom tap rather than Homebrew's
		// central API, so brew's local clone can be stale and a fetch or upgrade would
		// look at a version that no longer exists.
		if out, err := runTool(ctx, brew, "update"); err != nil {
			return Prepared{}, fmt.Errorf("update: brew update failed: %w (%s)", err, out)
		}
		if out, err := runTool(ctx, brew, "fetch", "--cask", caskName); err != nil {
			return Prepared{}, fmt.Errorf("update: brew fetch failed: %w (%s)", err, out)
		}
		return Prepared{}, nil

	case MethodWindowsInstaller:
		if dl == nil {
			return Prepared{}, fmt.Errorf("update: no downloader supplied for the windows installer")
		}
		path, err := dl(ctx)
		if err != nil {
			return Prepared{}, err
		}
		return Prepared{InstallerPath: path}, nil

	default:
		return Prepared{}, fmt.Errorf("update: %s installs cannot be prepared in place", method)
	}
}

// caskName is the cask this app is published as. It is a compile-time constant and
// never comes from release metadata — it reaches a command line.
const caskName = "openfortitray"

// runTool runs one of the fixed tools with a context and returns its combined
// output for the error message. The name always comes from an allowlist
// (resolveBrewPath), never from configuration or the network.
func runTool(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
