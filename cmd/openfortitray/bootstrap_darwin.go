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
// one-time install directly — no cross-thread marshaling is needed any more,
// since nothing here touches a Qt widget tree. Called on the UI goroutine from
// Connect (after the config-issue check has passed).
func (a *app) connectWithBootstrap() {
	go func() {
		if oft.HelperReady() {
			a.startTunnel()
			return
		}
		// Say why Connect did not dial. Without this the app simply sits there
		// after launch — the dialog is up, but the log shows nothing, and an idle
		// app with no explanation is indistinguishable from a hang.
		log.Printf("helper: not ready for this build (need ABI %d); offering to install it",
			oft.RequiredHelperABI)
		a.offerBootstrapInstall()
	}()
}

// offerBootstrapInstall shows the confirm dialog and, on OK, runs the privileged
// install off the UI thread, then dials on success or explains the failure and
// points at the manual installer. A dismissed password prompt
// (ErrUserCancelled) is intentionally silent — the user chose not to install.
//
// Dormant since the Wails migration: a.win is never set any more (Task 2
// stopped constructing a Qt window), so the nil-guard below always returns
// before the QMessageBox code beneath it is reached. Left in place — and its
// remaining a.dispatch.Post call converted to a direct call so it compiles —
// until Task 8 or a later task rewrites this dialog onto Wails.
func (a *app) offerBootstrapInstall() {
	if a.win == nil {
		log.Print("offerBootstrapInstall: no window available yet (Qt UI removed pending Task 8's Wails rewrite of this dialog); skipping the helper-install prompt — run scripts/install-helper.sh manually in the meantime")
		return
	}
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
	}()
}
