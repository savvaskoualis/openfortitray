//go:build darwin

package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	qt "github.com/mappu/miqt/qt6"

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
// one-time install — both marshalled back onto the UI goroutine via a.dispatch.
// Called on the UI goroutine from Connect (after the config-issue check has
// passed).
func (a *app) connectWithBootstrap() {
	go func() {
		if oft.HelperReady() {
			a.dispatch.Post(a.startTunnel)
			return
		}
		// Say why Connect did not dial. Without this the app simply sits there
		// after launch — the dialog is up, but the log shows nothing, and an idle
		// app with no explanation is indistinguishable from a hang.
		log.Printf("helper: not ready for this build (need ABI %d); offering to install it",
			oft.RequiredHelperABI)
		a.dispatch.Post(a.offerBootstrapInstall)
	}()
}

// offerBootstrapInstall shows the confirm dialog and, on OK, runs the privileged
// install off the UI thread, then dials on success or explains the failure and
// points at the manual installer. It runs on the UI goroutine (it mutates
// widgets). A dismissed password prompt (ErrUserCancelled) is intentionally
// silent — the user chose not to install. QMessageBox.Exec() is a blocking
// modal, which is fine here: this closure already runs on the Qt UI thread (via
// a.dispatch's drain timer), and a nested Qt event loop during exec() is
// standard practice, exactly as internal/settings' delete-profile confirm
// already does.
func (a *app) offerBootstrapInstall() {
	// Bring the window forward so the dialog has a visible parent (Connect can be
	// invoked from the tray while the window is hidden).
	a.win.Show()
	a.win.Raise()
	a.win.ActivateWindow()
	// The same gate covers a first install and an upgrade of an existing helper, so
	// the wording has to fit both: telling someone who has used the app for weeks
	// that it "needs to install" a helper reads like a mistake.
	title, body := "Install VPN helper",
		"OpenFortiTray needs to install a small helper to run the VPN.\n"+
			"This will ask for your Mac password. Install now?"
	if _, err := os.Stat(oft.HelperPath); err == nil {
		title = "Update VPN helper"
		body = "OpenFortiTray needs to update its VPN helper before it can connect.\n" +
			"This will ask for your Mac password. Update now?"
	}

	mb := qt.NewQMessageBox3(qt.QMessageBox__Question, title, body)
	mb.SetStandardButtons(qt.QMessageBox__Yes | qt.QMessageBox__No)
	if mb.Exec() != int(qt.QMessageBox__Yes) {
		return
	}

	go func() {
		err := oft.Install()
		a.dispatch.Post(func() {
			switch {
			case err == nil:
				a.startTunnel()
			case errors.Is(err, oft.ErrUserCancelled):
				// User dismissed the password prompt; nothing to report.
			default:
				errBox := qt.NewQMessageBox3(qt.QMessageBox__Critical, "Could not install the VPN helper",
					fmt.Sprintf("%v\n\nYou can install it manually by running scripts/install-helper.sh in a Terminal.", err))
				errBox.SetStandardButtons(qt.QMessageBox__Ok)
				errBox.Exec()
			}
		})
	}()
}
