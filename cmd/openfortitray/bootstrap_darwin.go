//go:build darwin

package main

import (
	"log"
	"os"

	oft "github.com/savvaskoualis/openfortitray"

	"github.com/savvaskoualis/openfortitray/internal/tray"
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

// offerBootstrapInstall tells the user a privileged helper install/update is
// needed, via a native OS notification, and points at the manual install
// script. It does NOT attempt an automated install or show a confirmation
// dialog — that UX decision (a real one-click install flow through the
// Wails UI) is out of scope here; this only keeps the existing "tell them
// to run scripts/install-helper.sh" behavior working now that the Qt
// dialog it used to show cannot exist anymore.
func (a *app) offerBootstrapInstall() {
	title, body := "VPN helper needed",
		"OpenFortiTray needs to install a small helper to run the VPN. "+
			"Run scripts/install-helper.sh in a Terminal, then try connecting again."
	if _, err := os.Stat(oft.HelperPath); err == nil {
		title = "VPN helper needs updating"
		body = "OpenFortiTray needs to update its VPN helper before it can connect. " +
			"Run scripts/install-helper.sh in a Terminal, then try connecting again."
	}
	log.Printf("bootstrap: %s — %s", title, body)
	tray.ShowMessage(title, body)
}
