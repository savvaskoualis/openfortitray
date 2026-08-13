//go:build darwin

package main

import (
	"errors"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	oft "github.com/savvaskoualis/openfortitray"
)

// installBootstrapHooks wires the first-run privileged-helper install into the
// app on macOS. A user who `brew install --cask`s the app has no helper and no
// sudoers rule until they run a terminal step; these hooks let the app install
// them itself behind one native admin-password prompt.
func (a *app) installBootstrapHooks() {
	a.connectBootstrap = a.connectWithBootstrap
	a.onPermanentError = a.offerBootstrapInstall
}

// connectWithBootstrap is the Connect gate on macOS. It probes helper readiness
// OFF the UI thread (the probe spawns `sudo -n`), then either dials or offers the
// one-time install — both marshalled back onto the UI goroutine. Called on the UI
// goroutine from Connect (after the config-issue check has passed).
func (a *app) connectWithBootstrap() {
	go func() {
		if oft.HelperReady() {
			fyne.Do(a.startTunnel)
			return
		}
		fyne.Do(a.offerBootstrapInstall)
	}()
}

// offerBootstrapInstall shows the confirm dialog and, on OK, runs the privileged
// install off the UI thread, then dials on success or explains the failure and
// points at the manual installer. It runs on the UI goroutine (it mutates
// widgets). A dismissed password prompt (ErrUserCancelled) is intentionally
// silent — the user chose not to install.
func (a *app) offerBootstrapInstall() {
	// Bring the window forward so the dialog has a visible parent (Connect can be
	// invoked from the tray while the window is hidden).
	a.win.Show()
	a.win.RequestFocus()
	dialog.ShowConfirm("Install VPN helper",
		"OpenFortiTray needs to install a small helper to run the VPN.\n"+
			"This will ask for your Mac password. Install now?",
		func(ok bool) {
			if !ok {
				return
			}
			go func() {
				err := oft.Install()
				fyne.Do(func() {
					switch {
					case err == nil:
						a.startTunnel()
					case errors.Is(err, oft.ErrUserCancelled):
						// User dismissed the password prompt; nothing to report.
					default:
						dialog.ShowError(fmt.Errorf(
							"Could not install the VPN helper: %w\n\n"+
								"You can install it manually by running scripts/install-helper.sh in a Terminal.", err),
							a.win)
					}
				})
			}()
		}, a.win)
}
