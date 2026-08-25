// Command openfortitray is the OpenFortiTray tray application.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/auth"
	"github.com/savvaskoualis/openfortitray/internal/autostart"
	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/shell"
	"github.com/savvaskoualis/openfortitray/internal/status"
	"github.com/savvaskoualis/openfortitray/internal/tray"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
	"github.com/savvaskoualis/openfortitray/internal/update"
	"github.com/savvaskoualis/openfortitray/internal/xopen"
)

// supervisor is the slice of *tunnel.Supervisor the app drives. Naming it as an
// interface lets the tests substitute a fake that records the teardown calls the
// graceful-shutdown path makes.
type supervisor interface {
	Connect()
	Disconnect()
	Wait(ctx context.Context)
}

// app adapts the packages to tray.App; it holds no logic of its own.
type app struct {
	cfg     *config.Config
	cfgDir  string
	sup     supervisor
	events  chan tunnel.Event
	logPath string

	fyneApp  fyne.App
	tray     *tray.Controller
	settings *settings.Controller
	// status is the connection panel; the shell decides when it is on screen.
	status *status.Controller
	// shell owns the single window and which section of it is visible.
	shell *shell.Shell
	// stopTick stops the 1 Hz uptime ticker that drives the status window's clock.
	// nil until the ticker is started in OnStarted; called once during teardown so
	// the goroutine cannot outlive the UI it posts to.
	stopTick func()
	// win is the (initially hidden) settings window, reused as the parent for the
	// first-run bootstrap dialogs. Set once in main after the window is built.
	win fyne.Window
	// connectBootstrap, when non-nil, runs the macOS first-run helper-install gate
	// before dialing: a passwordless-helper readiness probe, and — if the helper is
	// not yet installed — an admin-password prompt that installs it, then a dial. It
	// is nil off darwin and in tests, where Connect dials directly as before.
	// Installed by installBootstrapHooks (a no-op off macOS).
	connectBootstrap func()
	// onPermanentError, when non-nil, offers the same first-run install when the
	// supervisor reports a broken-install (ErrPermanent) event at runtime — e.g. the
	// sudoers rule was removed after being installed. nil off darwin and in tests.
	onPermanentError func()
	// onConnectIssue routes a blocking config issue to the settings window when
	// Connect is refused (see Connect). In production it is set to the settings
	// controller's ShowIssue once the window is built; it stays nil until then,
	// and tests substitute a recorder to assert Connect routes rather than dials.
	// It runs on the UI goroutine, where every Connect entry point calls it.
	onConnectIssue func(*settings.Issue)
	// quitting stops the event pump touching a tearing-down UI. It is set once,
	// on the UI goroutine, at the start of Quit; the pump reads it and, once set,
	// drains events without calling fyne.Do — so no fyne.Do is ever queued
	// against a UI that a.fyneApp.Quit() is about to destroy.
	quitting atomic.Bool
	// shutdownDone is closed once the teardown goroutine has finished, so
	// awaitShutdown can hold the process open until the tunnel is really down. It
	// is created and closed inside shutdownOnce, which both guarantees exactly one
	// close and publishes the assignment to whoever waits on it afterwards.
	shutdownDone chan struct{}
	// shutdownOnce makes the graceful teardown run exactly once, no matter how it
	// is triggered. Both the tray's Quit item and the OS-signal handler route
	// through app.shutdown, so a second trigger (a repeated SIGTERM, or a signal
	// arriving just as the user clicks Quit) cannot start the teardown twice.
	shutdownOnce sync.Once

	// tp is the snapshot of the active profile (and machine-wide paths) the
	// tunnel actually dials. Connect refreshes it from a.cfg on the UI goroutine
	// before starting the supervisor; the auth/run funcs read it from the
	// supervisor's goroutines. The mutex is what makes that cross-goroutine read
	// safe and is also what lets Save & Reconnect reach a running tunnel with the
	// newly edited settings (Connect re-snapshots the now-updated active profile).
	mu sync.Mutex
	tp tunnelParams

	// storedCookieTried gates the cache-first auth path to ONE stored-cookie
	// offer per Connect. startTunnel resets it to false before every
	// sup.Connect(); authenticate does a Swap(true) so the first authFn call this
	// Connect may reuse the stored cookie, while any later call (the supervisor
	// re-minting after the gateway rejected that cookie) is forced onto SAML. It
	// is crossed between the UI goroutine (reset) and the supervisor's (auth), so
	// it is atomic.
	storedCookieTried atomic.Bool
	// wantConnected records whether the app currently wants the tunnel up: true
	// from startTunnel, false from Disconnect. It exists so onSystemWake (called
	// from an OS thread, never the pump goroutine that owns lastNotified) has a
	// race-free answer to "should this reconnect" without touching pump-goroutine-
	// only state.
	wantConnected atomic.Bool
	// cookieGet/cookieSet/cookieDelete are the credstore seam. They default to the
	// package funcs in main(); tests substitute an in-memory fake so the cache-first
	// flow is exercised without touching the real keychain. samlAuth is the SAML
	// browser flow, likewise injectable so authenticate is unit-testable without a
	// live gateway.
	cookieGet    func(key string) (string, error)
	cookieSet    func(key, value string) error
	cookieDelete func(key string) error
	samlAuth     func(ctx context.Context, prof config.Profile) (string, error)
	// cookieRetryInterval/cookieRetryWindow default to the package constants of
	// the same name and are overridden in tests so a retry loop does not
	// actually sleep. See cookieGetWithRetry.
	cookieRetryInterval time.Duration
	cookieRetryWindow   time.Duration

	// notify posts a desktop notification. It is the fyne app's
	// SendNotification in production and a recorder in tests; nil means "no
	// notifications" (the pump null-checks it). Only the pump goroutine calls
	// it, via notifyFor, so no extra synchronisation is needed.
	notify func(title, body string)
	// lastNotified is the state the last notification described, so the pump
	// notifies on TRANSITIONS only — the supervisor re-emits the same state on
	// every retry round, and one toast per backoff tick is exactly the noise
	// this is meant to avoid. Pump-goroutine-only; not part of the UI state.
	lastNotified tunnel.State
	// retryNotified records that the CURRENT retry episode has already produced a
	// notification, so a reconnect storm posts one toast rather than one per round.
	// Cleared when the episode ends: Connected, Disconnected, or the terminal Error.
	// Pump-goroutine-only, like lastNotified.
	retryNotified bool

	// updateMu guards updateRel and lastPromptedTag: the newest release the
	// background checker found to be newer than this build, or nil until one is
	// found. UpdateClicked reads updateRel to decide between applying a pending
	// update and kicking off a fresh check.
	updateMu  sync.Mutex
	updateRel *update.Release
	// lastPromptedTag is the release tag the update dialog was last shown for, so
	// the 6-hourly re-check surfaces the popup only ONCE per distinct version (the
	// badge + menu item update every check; the dialog does not nag). Guarded by
	// updateMu; consulted via shouldPromptUpdate.
	lastPromptedTag string
}

// tunnelParams is the subset of config the tunnel dials with, snapshotted so the
// supervisor's goroutines never read the live *config.Config the settings window
// may be rewriting on the UI goroutine.
type tunnelParams struct {
	prof            config.Profile
	openconnectPath string
	helperPath      string
}

func (a *app) snapshot() tunnelParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tp
}

func (a *app) setSnapshot(tp tunnelParams) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tp = tp
}

// Connect starts the tunnel, unless the active profile has a blocking config
// issue. The gateway, for instance, has no built-in default (it is
// deployment-specific), so a fresh install would otherwise hand openconnect a
// bare ":10443" — the SAML browser window would open against a nonexistent host
// and the failure would surface as an opaque connection error.
//
// Rather than start a doomed tunnel and report a red Error, Connect asks
// settings.FirstConnectIssue what would go wrong and, if anything would, routes
// the user to the exact fix: the settings window opens on the right tab with the
// offending field focused and flagged, and a banner names the next action (see
// Controller.ShowIssue). No JSON editing, no cryptic error. Only when the active
// profile is ready to dial does it snapshot the profile and start the tunnel.
//
// It runs on the UI goroutine — the tray's Connect item, the window's Connect
// button and the autostart-at-launch call all reach it there — so routing to the
// window is a direct call, not a marshalled one.
func (a *app) Connect() {
	if issue := settings.FirstConnectIssue(a.cfg); issue != nil {
		log.Printf("connect deferred: %s (%s ▸ %s)", issue.Message, issue.Tab, issue.Field)
		if a.onConnectIssue != nil {
			a.onConnectIssue(issue)
		}
		return
	}
	// First-run bootstrap gate (macOS): if the privileged helper is not yet
	// installed, offer to install it via one admin-password prompt before dialing.
	// connectBootstrap owns the readiness probe, the prompt, the off-thread install
	// and the follow-on dial. Off darwin and in tests it is nil and we dial
	// directly, keeping the existing behaviour.
	if a.connectBootstrap != nil {
		a.connectBootstrap()
		return
	}
	a.startTunnel()
}

// startTunnel snapshots the active profile the tunnel will dial — so a Connect
// that follows a settings Save picks up the edited profile rather than the
// startup one — and starts the supervisor. It runs on the UI goroutine (every
// caller reaches it there); the supervisor's goroutines read only the snapshot.
func (a *app) startTunnel() {
	prof := *a.cfg.Active()
	a.setSnapshot(tunnelParams{
		prof:            prof,
		openconnectPath: resolveOpenconnectPath(a.cfg.OpenconnectPath),
		helperPath:      a.cfg.HelperPath,
	})
	// Fresh Connect: allow authenticate to offer the stored cookie once (no
	// browser). Reset BEFORE sup.Connect() so the reset happens-before the
	// supervisor's first authFn call reads it. A later re-mint within this same
	// Connect (gateway rejected the stored cookie) finds the flag set and runs SAML.
	a.storedCookieTried.Store(false)
	a.wantConnected.Store(true)
	a.sup.Connect()
}

// onSystemWake forces a fresh reconnect after the OS reports the machine resumed
// from sleep — see watchSystemSleep. It exists because openconnect's own dead-peer
// detection is comparatively slow (this gateway explicitly disables openconnect's
// self-managed reconnect, so a stale post-sleep tunnel is only caught once a
// keepalive round trip times out), which can leave the tray looking "Connected" to
// a session that has been dead since before the laptop went to sleep. Forcing an
// immediate Disconnect+Connect is exactly what the tray's own Disconnect-then-
// Connect already does when a user does it manually — this only automates that.
//
// wantConnected, not lastNotified, answers "was this connected": lastNotified is
// documented pump-goroutine-only, and the OS delivers this callback on its own
// thread (a Cocoa notification queue, a Windows callback thread, or the D-Bus
// goroutine) — never the pump.
func (a *app) onSystemWake() {
	if !a.wantConnected.Load() {
		return
	}
	log.Print("openfortitray: resumed from sleep; forcing a fresh reconnect")
	fyne.DoAndWait(func() {
		a.Disconnect()
		a.Connect()
	})
}

// cookieKey namespaces the stored SVPNCOOKIE by gateway host, so different
// profiles/gateways keep independent cookies.
func cookieKey(gateway string) string { return "openfortitray:" + gateway }

// cookieGetWithRetry reads the stored cookie, retrying while the store reports
// credstore.ErrBusy (the OS secret store cannot answer yet — on macOS, most
// often the login keychain mid-unlock) for up to cookieRetryWindow. Any other
// result, including a clean miss, returns immediately.
//
// It exists because a macOS Login Item can start before the login keychain's
// automatic unlock finishes. Without this, that race made a perfectly valid
// stored session look like a miss on every such launch, so "connect at login"
// fell back to an interactive SAML browser flow the user never asked for and,
// running under an LSUIElement app, may not even notice appearing.
func (a *app) cookieGetWithRetry(ctx context.Context, key string) (string, error) {
	deadline := time.Now().Add(a.cookieRetryWindow)
	for {
		cookie, err := a.cookieGet(key)
		if !errors.Is(err, credstore.ErrBusy) || time.Now().After(deadline) {
			return cookie, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(a.cookieRetryInterval):
		}
	}
}

// authenticate is the supervisor's AuthFunc. It is cache-first: on a fresh
// Connect (storedCookieTried just reset by startTunnel) it offers the profile's
// stored cookie ONCE, with no browser, so a still-valid session reconnects
// silently and survives app restarts. The supervisor re-mints on ErrAuthRejected
// (a dead stored cookie costs one silent openconnect attempt), which calls this
// again with the flag already set — forcing the SAML browser flow, whose fresh
// cookie is then stored.
//
// It runs on the supervisor's goroutine. The cookie value is never logged (only
// the fact of reuse) and never written to config.json.
func (a *app) authenticate(ctx context.Context) (string, error) {
	prof := a.snapshot().prof
	gw := prof.Gateway
	key := cookieKey(gw)

	// Offer the stored cookie only on the first auth of this Connect, only when
	// the profile opts in, and only for a real gateway. Swap(true) both consumes
	// the one-shot and marks it used for any later re-mint this Connect.
	if prof.RememberSession && gw != "" && !a.storedCookieTried.Swap(true) {
		if cookie, err := a.cookieGetWithRetry(ctx, key); err != nil {
			log.Printf("auth: could not read stored session cookie: %v", err)
		} else if cookie != "" {
			log.Print("auth: reusing stored session cookie (no browser)")
			return cookie, nil
		}
	}

	// Reaching the SAML flow after the stored cookie was already offered this
	// Connect means the gateway rejected it. Such a cookie is dead for good — a
	// FortiGate SVPNCOOKIE is bound to its server-side session, so it cannot start
	// working later — and keeping it only buys a guaranteed-failed openconnect
	// attempt on every future Connect. Drop it so the next Connect goes straight to
	// SAML. Best-effort: failing to delete must not fail the login.
	if gw != "" && a.storedCookieTried.Load() {
		if err := a.cookieDelete(key); err != nil {
			log.Printf("auth: could not drop the rejected stored session cookie: %v", err)
		}
	}

	cookie, err := a.samlAuth(ctx, prof)
	if err != nil {
		return "", err
	}
	// Persist the freshly minted cookie for the next reconnect/restart. Best-effort:
	// a store failure must not fail an otherwise-good login.
	if prof.RememberSession && gw != "" {
		if err := a.cookieSet(key, cookie); err != nil {
			log.Printf("auth: could not store session cookie: %v", err)
		}
	}
	return cookie, nil
}

// defaultSAMLAuth is the production SAML browser flow, unchanged from the inline
// authFn it replaced: it opens the browser, waits for the redirect, and returns
// the minted SVPNCOOKIE. Injectable via app.samlAuth so authenticate is testable
// without a live gateway.
func defaultSAMLAuth(ctx context.Context, prof config.Profile) (string, error) {
	authr := &auth.Authenticator{
		GatewayURL: prof.GatewayURL(),
		ListenPort: prof.SAMLPort,
		Client:     &http.Client{Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	log.Printf("auth: starting SAML login on 127.0.0.1:%d", prof.SAMLPort)
	cookie, err := authr.Authenticate(ctx)
	if err != nil {
		log.Printf("auth: failed: %v", err)
		return "", err
	}
	log.Printf("auth: cookie obtained")
	return cookie, nil
}

// defaultOpenconnectName is the bare, PATH-resolved value internal/config ships
// as OpenconnectPath's default (config.defaults()). Only this exact value is
// eligible for the Windows bundled-binary override below; any explicit user path
// is left untouched.
const defaultOpenconnectName = "openconnect"

// resolveOpenconnectPath returns the openconnect binary the tunnel should run.
//
// Windows only: openconnect is bundled beside the tray exe (the installer and
// install.ps1 place it at <exeDir>\openconnect\openconnect.exe) because there is
// no reliable way to get openconnect onto a locked-down Cloud PC — winget is a
// dead stub there and nothing is on PATH. When the configured path is still the
// bare "openconnect" default (the user has not set an explicit path) and that
// bundled binary exists, it is used so the install is turnkey. An explicit
// user-set path, a missing bundle, an os.Executable() failure, or any non-Windows
// OS all leave the configured value unchanged (macOS/Linux go through the
// privileged helper, which resolves openconnect itself, so this must never fire
// there). Derived from os.Executable() so it works wherever the app is installed;
// no hardcoded Program Files path.
func resolveOpenconnectPath(configured string) string {
	if runtime.GOOS != "windows" {
		return configured
	}
	exe, err := os.Executable()
	if err != nil {
		return configured
	}
	return resolveBundledOpenconnect(configured, filepath.Dir(exe), func(p string) bool {
		info, statErr := os.Stat(p)
		return statErr == nil && !info.IsDir()
	})
}

// resolveBundledOpenconnect is the OS-independent core of resolveOpenconnectPath,
// split out so it is unit-testable off Windows: exeDir is the directory of the
// running executable and exists reports whether a regular file is present there.
func resolveBundledOpenconnect(configured, exeDir string, exists func(string) bool) string {
	if configured != defaultOpenconnectName {
		return configured
	}
	bundled := filepath.Join(exeDir, "openconnect", "openconnect.exe")
	if exists(bundled) {
		return bundled
	}
	return configured
}

func (a *app) Disconnect() {
	a.wantConnected.Store(false)
	a.sup.Disconnect()
}
func (a *app) AutostartEnabled() bool { return autostart.IsEnabled() }
func (a *app) LogPath() string        { return a.logPath }

// version is stamped at build time via -ldflags "-X main.version=<tag>"
// (Makefile / .github/workflows/release.yml). Unstamped local builds report "dev".
var version = "dev"

// Version returns the build version string shown in the tray header.
func (a *app) Version() string { return version }

// ShowSettings reveals the settings window (tray.App). It is built once at
// startup; this only shows the existing, hidden window.
func (a *app) ShowSettings() {
	if a.settings == nil || a.shell == nil {
		return
	}
	// Re-sync the form from the live config before it is shown, discarding edits
	// abandoned last time.
	a.settings.Show()
	a.shell.Reveal(shell.SectionConnection)
}

// ShowStatus reveals the status window (tray.App / the Status… item). Like the
// settings window it is built once at startup and hidden.
func (a *app) ShowStatus() {
	if a.shell != nil {
		a.shell.Reveal(shell.SectionStatus)
	}
}

// OpenLog opens the log file in the platform's default handler (status.Host). The
// tray has its own "View logs" row doing the same thing; the window offers it at
// the foot of the activity list, where someone reading the history is already
// looking for more detail.
func (a *app) OpenLog() {
	if err := xopen.File(a.logPath); err != nil {
		log.Printf("open log: %v", err)
	}
}

// GatewayLabel is the active profile's "host:port" for the status window's detail
// card (status.Host). It returns an empty string when no gateway is configured, so
// the card renders a dash rather than a stray colon.
// It reads Profile.Port directly, which is the EFFECTIVE port: settings
// normalises it through effectivePort on every commit, so a profile without a
// custom port already holds the default rather than a zero.
func (a *app) GatewayLabel() string {
	p := a.cfg.Active()
	if p == nil || p.Gateway == "" {
		return ""
	}
	return net.JoinHostPort(p.Gateway, strconv.Itoa(p.Port))
}

// DTLSLabel reports the active profile's DTLS preference for the detail card
// (status.Host). DTLS is the UDP transport openconnect prefers; when it is off the
// tunnel runs over TLS only, which is slower but survives a firewall that drops
// UDP 10443 — worth showing, because that difference is user-visible.
func (a *app) DTLSLabel() string {
	p := a.cfg.Active()
	if p != nil && p.DTLS {
		return "DTLS on"
	}
	return "DTLS off"
}

// Config returns the live configuration for the settings window to clone
// (settings.Host). It runs on the UI goroutine.
func (a *app) Config() *config.Config { return a.cfg }

// Commit takes the settings window's edited config, syncs the OS autostart login
// item to c.Autostart, persists c, and makes it the live config (settings.Host).
// It runs on the UI goroutine; the tunnel reads only the snapshot taken at
// Connect, so replacing a.cfg here does not race the supervisor.
func (a *app) Commit(c *config.Config) error {
	if c.Autostart != autostart.IsEnabled() {
		if err := setLoginItem(c.Autostart); err != nil {
			log.Printf("autostart: %v", err)
			return err
		}
	}
	if err := c.Save(a.cfgDir); err != nil {
		log.Printf("config save: %v", err)
		return err
	}
	// A stored cookie outlives Disconnect/Quit (that is the whole point), but a
	// few edits make an existing one useless or unwanted, so delete it here —
	// while a.cfg still holds the OLD profiles to compare against.
	a.reconcileStoredCookies(a.cfg, c)
	*a.cfg = *c
	return nil
}

// reconcileStoredCookies deletes stored session cookies that the edit from old to
// new makes stale or unwanted. For each new profile matched by name to an old
// one, the old gateway's cookie is deleted when: remember-session was turned off,
// the gateway changed, or the auth method changed — a cookie for a changed target
// (or one the user opted out of) is worthless and must not linger at rest. It
// runs on the UI goroutine (from Commit); deletion is best-effort and only
// logged. Matching by name means a rename is treated as a new profile and its old
// cookie is left to age out under its own gateway key, which is harmless.
func (a *app) reconcileStoredCookies(old, new *config.Config) {
	if a.cookieDelete == nil {
		return
	}
	oldByName := make(map[string]config.Profile, len(old.Profiles))
	for _, p := range old.Profiles {
		oldByName[p.Name] = p
	}
	for _, np := range new.Profiles {
		op, ok := oldByName[np.Name]
		if !ok {
			continue
		}
		turnedOff := !np.RememberSession && op.RememberSession
		gatewayChanged := op.Gateway != np.Gateway
		authChanged := op.Auth.Method != np.Auth.Method
		if !np.RememberSession || turnedOff || gatewayChanged || authChanged {
			// Delete the cookie under the OLD gateway (the one it was stored for).
			// Idempotent: deleting an absent key is a no-op.
			if op.Gateway != "" {
				if err := a.cookieDelete(cookieKey(op.Gateway)); err != nil {
					log.Printf("auth: could not delete stored session cookie: %v", err)
				}
			}
		}
	}
}

// updateRepo is the GitHub repo the updater checks and downloads from.
const updateRepo = "savvaskoualis/openfortitray"

// releasesPageURL is opened as the manual fallback whenever an automatic update
// is not possible or fails (non-brew mac, Linux, a download/verify error).
const releasesPageURL = "https://github.com/savvaskoualis/openfortitray/releases/latest"

// updateChecker builds the release checker with a bounded HTTP client.
func updateChecker() update.Checker {
	return update.Checker{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Repo:       updateRepo,
	}
}

// startUpdateChecker polls GitHub for a newer release: once ~30s after launch,
// then every 6h. All failures are logged and ignored — a background check must
// never disrupt the app. It exits when ctx is cancelled.
func (a *app) startUpdateChecker(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		a.checkForUpdate(ctx, false)
		timer.Reset(6 * time.Hour)
	}
}

// checkForUpdate runs one release check; if a newer version exists it records the
// release, badges the tray + relabels its update item, and — only ONCE per
// distinct version — pops the update dialog. The badge/menu update on every check
// (cheap, idempotent); the dialog shows once per new tag so the 6-hourly re-check
// does not nag. All UI runs on the UI goroutine and honours the quitting gate.
func (a *app) checkForUpdate(ctx context.Context, manual bool) {
	rel, err := updateChecker().Available(ctx, version)
	if err != nil {
		log.Printf("update: check failed: %v", err)
		// A background check that fails stays quiet — the next one is in six hours and
		// a transient network error is not news. A check the user ASKED for must answer,
		// or the menu item looks broken.
		if manual {
			a.reportCheckResult("Could not check for updates", err.Error())
		}
		return
	}
	if rel == nil {
		if manual {
			a.reportCheckResult("You are up to date",
				"OpenFortiTray "+version+" is the latest version.")
		}
		return
	}
	// On the Homebrew path the check and the apply read different sources: this
	// release exists on GitHub, but `brew upgrade --cask` can only install what the
	// tap's cask points at. Offering an update the cask has not caught up to yet
	// produces brew's "the latest version is already installed" and no upgrade —
	// which is indistinguishable, from the user's side, from a broken updater. So
	// stay quiet until the cask is ready (the tap bumps itself within the hour of a
	// release); the next 6-hourly check picks it up. Fails open on a read error, so
	// an unreachable tap cannot suppress a real update.
	if update.InstallMethod() == update.MethodHomebrew {
		cc := update.CaskChecker{HTTPClient: &http.Client{Timeout: 30 * time.Second}}
		if !update.CaskHasTag(ctx, cc, rel.Tag) {
			log.Printf("update: %s is published but the Homebrew cask is not bumped yet; waiting", rel.Tag)
			if manual {
				a.reportCheckResult("Update not ready yet",
					"OpenFortiTray "+rel.Tag+" has been released, but the Homebrew cask has not "+
						"caught up. It usually does within the hour, and the app will offer the "+
						"update then.")
			}
			return
		}
	}
	a.updateMu.Lock()
	a.updateRel = rel
	a.updateMu.Unlock()
	if a.quitting.Load() {
		return
	}
	prompt := manual || a.shouldPromptUpdate(rel.Tag)
	fyne.Do(func() {
		if a.quitting.Load() {
			return
		}
		if a.tray != nil {
			a.tray.SetUpdateAvailable(rel.Tag)
		}
		if prompt {
			a.promptUpdate(rel)
		}
	})
}

// shouldPromptUpdate reports whether the update dialog should be shown for tag,
// recording it so the same version is never prompted twice. A new (distinct,
// non-empty) tag prompts once; a repeat of the last-prompted tag, or an empty
// tag, does not. It is a pure decision guarded by updateMu, unit-tested directly
// (wiring a headless fyne dialog in a test is impractical, so the actual
// dialog.Show lives in the thin promptUpdate wrapper).
func (a *app) shouldPromptUpdate(tag string) bool {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if tag == "" || tag == a.lastPromptedTag {
		return false
	}
	a.lastPromptedTag = tag
	return true
}

// promptUpdate opens the update flow: the offer, then the download, then the
// request to restart. See updateflow.go.
func (a *app) promptUpdate(rel *update.Release) {
	if a.fyneApp == nil {
		return
	}
	newUpdateFlow(a, rel).start()
}

// reportCheckResult answers a MANUAL check for updates. A click has to produce a
// visible result whatever the answer — "no update" reported as silence is
// indistinguishable from a menu item that does nothing, which is precisely how this
// one read.
func (a *app) reportCheckResult(heading, body string) {
	if a.fyneApp == nil {
		return
	}
	fyne.Do(func() {
		if a.quitting.Load() {
			return
		}
		w := a.fyneApp.NewWindow("OpenFortiTray Update")
		w.SetFixedSize(true)
		w.Resize(fyne.NewSize(420, 200))
		w.CenterOnScreen()
		w.SetCloseIntercept(w.Hide)

		h := canvas.NewText(heading, theme.Color(theme.ColorNameForeground))
		h.TextSize = theme.Size(theme.SizeNameSubHeadingText)
		h.TextStyle = fyne.TextStyle{Bold: true}
		msg := widget.NewLabel(body)
		msg.Wrapping = fyne.TextWrapWord
		msg.Importance = widget.LowImportance
		ok := widget.NewButton("OK", func() { w.Hide() })
		ok.Importance = widget.HighImportance

		w.SetContent(container.NewPadded(container.NewVBox(
			h, msg, layout.NewSpacer(),
			container.NewHBox(layout.NewSpacer(), ok),
		)))
		w.Show()
		w.RequestFocus()
	})
}

// UpdateClicked is the tray update item's action (UI goroutine). With a pending
// update it applies it; otherwise it kicks off an immediate background check.
func (a *app) UpdateClicked() {
	a.updateMu.Lock()
	rel := a.updateRel
	a.updateMu.Unlock()
	if rel == nil {
		go a.checkForUpdate(context.Background(), true)
		return
	}
	a.promptUpdate(rel)
}

// windowsUpdateAssets picks the Setup.exe and SHA256SUMS assets from a release,
// returning nil for either if it is absent.
func windowsUpdateAssets(rel *update.Release) (setup, sums *update.Asset) {
	for i := range rel.Assets {
		as := &rel.Assets[i]
		switch {
		case strings.HasPrefix(as.Name, "OpenFortiTray-") && strings.HasSuffix(as.Name, "-Setup.exe"):
			setup = as
		case as.Name == "SHA256SUMS":
			sums = as
		}
	}
	return setup, sums
}

// startUptimeTicker drives the status window's session clock, the one thing on
// screen that changes without a tunnel event.
//
// It is started from OnStarted rather than from main because it posts through
// fyne.Do, and it is stopped during teardown: a ticker goroutine that outlived
// the UI would queue work against a driver Quit is destroying — the same hazard
// the pump's quitting flag guards against, so it reads that flag too.
//
// status.Tick returns on a branch when no session is up, so an idle app pays for
// a channel receive per second and nothing else.
func (a *app) startUptimeTicker() {
	if a.status == nil || a.stopTick != nil {
		return
	}
	t := time.NewTicker(time.Second)
	done := make(chan struct{})
	a.stopTick = func() { close(done) }
	go func() {
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if a.quitting.Load() {
					return
				}
				fyne.Do(func() {
					if a.quitting.Load() || a.status == nil {
						return
					}
					a.status.Tick()
				})
			}
		}
	}()
}

// pump is the one goroutine that reads tunnel events and drives the UI. fyne
// owns the main thread, so every mutation of a fyne object from here is
// marshalled onto the UI goroutine with fyne.Do. Once quitting is set the pump
// keeps draining the channel (so the supervisor's teardown events never block)
// but stops touching the UI, which a.fyneApp.Quit() is about to destroy.
func (a *app) pump() {
	for e := range a.events {
		if a.quitting.Load() {
			continue
		}
		e := e
		// Notify before the UI hop: notifyFor is pure bookkeeping plus one
		// SendNotification, both safe off the UI goroutine, and doing it here
		// keeps it out of the fyne.Do closure that a teardown can skip.
		a.notifyFor(e)
		fyne.Do(func() {
			// Re-check inside the closure: the pre-check above is not atomic with
			// fyne.Do, and once fyne has drained its queue fyne.Do runs the closure
			// inline on this goroutine, so an event slipping past the pre-check just
			// as Quit tears the driver down could otherwise call Apply against a
			// terminated UI (§7.8). Belt-and-suspenders with the pre-check.
			if a.quitting.Load() {
				return
			}
			a.tray.Apply(e)
			// Same consumer, same fyne.Do: mirror the status onto the settings
			// window's live strip. Safe whether the window is shown or hidden.
			if a.settings != nil {
				a.settings.Apply(e)
			}
			// And onto the status window, in the SAME closure as the other two, so
			// all three surfaces render one event or none of them do. Updating a
			// hidden window's widgets is safe and cheap.
			if a.status != nil {
				a.status.Apply(e)
			}
			// A terminal, broken-install failure (tunnel.ErrPermanent, whose
			// Error() text carries "install is broken") means the privileged path
			// is not set up — on macOS, offer the same one-prompt install rather
			// than leaving the user staring at a red Error. onPermanentError is nil
			// off darwin and in tests, so this is a no-op there.
			if a.onPermanentError != nil && e.State == tunnel.Error && strings.Contains(e.Detail, "install is broken") {
				a.onPermanentError()
			}
		})
	}
}

// notifyFor posts a desktop notification for the events a user actually wants
// to be interrupted for, and only on a state TRANSITION.
//
// The menu-bar icon already carries the live state, so a notification is for
// the moments the user is looking at another window:
//
//   - Connected — the tunnel came up (with the assigned IP, when we have one).
//   - Reconnecting — an established tunnel dropped. Notified only when the
//     previous notified state was Connected, so the retry rounds that follow
//     (the supervisor re-emits Reconnecting on every backoff tick) stay silent,
//     and a reconnect that never got connected in the first place never fires.
//   - Error — terminal: the session was taken, sign-in didn't complete, or the
//     install is broken. Always worth surfacing; Detail is already the short
//     human text friendlyDetail produced (never raw openconnect stderr).
//
// Disconnected is deliberately silent: the user asked for it.
func (a *app) notifyFor(e tunnel.Event) {
	if a.notify == nil {
		return
	}
	prev := a.lastNotified
	if e.State == prev {
		return
	}
	// Track every state we see, notified or not, so "was connected" is answered
	// by the real previous state rather than the last state that made a sound.
	a.lastNotified = e.State

	var title, body string
	switch e.State {
	case tunnel.Connected:
		a.retryNotified = false
		title = "VPN connected"
		body = "Tunnel is up."
		if e.Detail != "" {
			body = "Tunnel is up (" + e.Detail + ")."
		}

	case tunnel.Reconnecting:
		// One notification per RETRY EPISODE, not per Reconnecting event.
		//
		// The transition check above is not enough on its own: the supervisor's
		// retry rounds alternate Reconnecting → Connecting → Reconnecting, so each
		// Reconnecting looks like a fresh transition and an ungated notify would
		// post one toast per round. retryNotified is cleared only when the episode
		// actually ends — on Connected, on a user Disconnect, or on the terminal
		// Error — so a reconnect storm produces exactly one toast.
		if a.retryNotified {
			log.Printf("notify: %v suppressed (already notified this retry episode)", e.State)
			return
		}
		a.retryNotified = true
		if prev == tunnel.Connected {
			title = "VPN dropped"
			body = "Reconnecting…"
		} else {
			// A retry that never had a healthy session. This used to stay silent, on
			// the reasoning that the menu-bar icon already shows it trying — but the
			// icon is a colour in the corner of the screen, and a connect that
			// quietly retries for a minute and a half before giving up reads as the
			// app having done nothing at all. It says something different from the
			// drop case because nothing was dropped.
			title = "VPN reconnecting"
			body = "Couldn't connect — retrying."
			if e.Detail != "" {
				body = e.Detail
			}
		}

	case tunnel.Error:
		a.retryNotified = false
		title = "VPN disconnected"
		body = e.Detail
		if body == "" {
			body = "Connection failed — open openfortitray to retry."
		}

	default:
		// Disconnected is the user's own doing, and Connecting/Authenticating are
		// expected steps they just triggered. Silent — but the episode ends here, so
		// the next retry is allowed to speak.
		if e.State == tunnel.Disconnected {
			a.retryNotified = false
		}
		log.Printf("notify: %v is not a notifying state", e.State)
		return
	}

	// Logged because SendNotification reports nothing back: it cannot fail visibly,
	// so without this line a missing toast is indistinguishable from a toast the app
	// never tried to post. That ambiguity cost real debugging time — on macOS the
	// authorization failure is logged by fyne through NSLog, which only reaches this
	// file because redirectStderr repoints fd 2 (see redirect_unix.go).
	log.Printf("notify: posting %q — %q", title, body)
	a.notify(title, body)
}

// Quit is invoked from the tray's Quit item on the UI goroutine. It routes to the
// shared graceful shutdown, which quits the fyne app once the tunnel is down.
func (a *app) Quit() { a.shutdown(func() { fyne.Do(a.fyneApp.Quit) }) }

// shutdown tears the tunnel down and then calls done to leave the process. It is
// the one graceful-exit path: the tray's Quit item and the OS-signal handler both
// route here, so a launchctl bootout / kill -TERM / Ctrl-C tears the tunnel down
// through the root helper's "stop" — which CAN signal the root openconnect —
// exactly as a menu Quit does, instead of orphaning it. Without this an abrupt
// signal skipped the teardown entirely (the Fyne 2 review flagged this) and left
// a root openconnect holding the one-per-user FortiGate session.
//
// It mirrors the old Quit routine's structure: sup.Wait can block for
// shutdownWait, so the teardown runs on a worker goroutine rather than the UI
// goroutine (blocking that would freeze the menu bar); quitting is set first so
// the event pump stops touching a UI that is about to be destroyed; and the
// teardown runs at most once (shutdownOnce). done differs by caller only in how
// the process is left — the tray and signal paths both quit the fyne app, which
// unblocks a.fyneApp.Run() in main.
//
// Residual limitation: a true SIGKILL (or power loss) of the APP cannot run any
// in-process teardown, so this path never executes and the root openconnect is
// orphaned. The recovery for that case is the startup self-heal
// (selfHealThenConnect → tunnel.ReapStale), which reaps the orphan on the next
// launch. The only complete fix would be an out-of-process watchdog — a launchd
// KeepAlive/watchdog that reaps openconnect on app death — which is out of scope
// here.
func (a *app) shutdown(done func()) {
	a.shutdownOnce.Do(func() {
		a.shutdownDone = make(chan struct{})
		a.quitting.Store(true)
		// Stop the uptime ticker before the teardown begins, so it cannot queue a
		// fyne.Do against a driver that is about to be destroyed. quitting is already
		// set, so an in-flight tick returns without touching the UI either way; this
		// just stops the goroutine rather than leaving it running to no purpose.
		if a.stopTick != nil {
			a.stopTick()
			a.stopTick = nil
		}
		go func() {
			// Signal completion no matter how this returns, so awaitShutdown (which
			// keeps the process alive for exactly this work) can never wait out its
			// full timeout on a teardown that already finished or panicked.
			defer close(a.shutdownDone)
			a.sup.Disconnect()
			ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
			defer cancel()
			a.sup.Wait(ctx)
			if ctx.Err() != nil {
				log.Printf("openfortitray: backend did not stop within %s", shutdownWait)
			}
			log.Printf("openfortitray: exiting")
			done()
		}()
	})
}

// awaitShutdown blocks until the tunnel teardown has finished, starting it if
// nothing has yet.
//
// It exists because fyne installs its OWN SIGINT/SIGTERM handler
// (gLDriver.catchTerm) which calls Quit as soon as a signal arrives. Go delivers a
// signal to every registered channel, so a SIGTERM reaches both handlers at once:
// ours begins the graceful teardown on a goroutine, while fyne's ends the run
// loop. main then returned and the process died mid-teardown — openconnect never
// got to send its clean logout, so the FortiGate kept the session and refused
// every new cookie (for minutes) until it timed the session out server-side. That
// looked exactly like "we get logged out a lot" and like a connect that will not
// connect. The observable symptom in the log was a "tearing down" line with no
// matching "tunnel: exited" or "exiting" line after it.
//
// Called after Run returns, so the process outlives the UI by as long as the
// teardown needs. shutdown is once-guarded, so calling it here is safe whether
// the exit came from the tray's Quit, a signal, or fyne's own handler; the done
// callback is a no-op because the run loop has already ended.
func (a *app) awaitShutdown() {
	a.shutdown(func() {})
	select {
	case <-a.shutdownDone:
	case <-time.After(shutdownWait + 5*time.Second):
		// The teardown owns its own bounded wait; this is only a backstop so a
		// wedged helper cannot keep the process alive forever.
		log.Printf("openfortitray: teardown did not finish in time; exiting anyway")
	}
}

// watchSignals routes SIGINT/SIGTERM/SIGHUP to the same graceful shutdown the
// tray Quit uses, so `launchctl bootout` (SIGTERM from launchd stop), `kill
// -TERM`, `pkill` and Ctrl-C all tear the tunnel down cleanly instead of leaving
// a root openconnect the unprivileged parent cannot signal. It loops rather than
// returning after the first signal so a second signal is observed too — though
// shutdown is once-guarded, so the second is a no-op. quit is what leaves the
// process (a.fyneApp.Quit in production).
func (a *app) watchSignals(sigs <-chan os.Signal, quit func()) {
	for s := range sigs {
		log.Printf("openfortitray: received signal %s, tearing down", s)
		a.shutdown(quit)
	}
}

// resumeMarkerName is the one-shot file finishUpdate leaves in cfgDir when the
// tunnel was up at restart time, and the next launch consumes to reconnect —
// see shouldResumeAfterUpdate and consumeResumeMarker.
const resumeMarkerName = "resume-connect"

// shouldResumeAfterUpdate reports whether finishUpdate should leave a resume
// marker for the next launch: only when the tunnel was actually connected at
// restart time, so an update offered (and declined, or applied while
// disconnected) never fabricates a reconnect the user never had.
//
// lastNotified already tracks every tunnel state seen, not just the ones that
// notified (see notifyFor), so this needs no extra bookkeeping.
func (a *app) shouldResumeAfterUpdate() bool { return a.lastNotified == tunnel.Connected }

// writeResumeMarker leaves the one-shot marker consumeResumeMarker looks for on
// the next launch. Best-effort: a write failure only costs the user a manual
// reconnect after this particular update, which is what happened before this
// existed at all.
func writeResumeMarker(cfgDir string) error {
	return os.WriteFile(filepath.Join(cfgDir, resumeMarkerName), nil, 0o600)
}

// consumeResumeMarker reports whether an update-triggered relaunch left a
// resume marker for this launch, removing it either way so it fires at most
// once. It is deliberately a SEPARATE signal from cfg.Autostart: that setting
// registers the OS login item and answers "connect every time the app
// launches", which is a different question from "this particular launch is
// resuming a session that was up when finishUpdate tore it down a moment ago".
// Conflating the two used to mean a user who had disabled autostart (a login
// item preference, not an update-recovery opt-out) lost their VPN, with no
// automatic recovery, after every single update — even though the restart
// prompt promised it "comes back on its own".
func consumeResumeMarker(cfgDir string) bool {
	p := filepath.Join(cfgDir, resumeMarkerName)
	_, err := os.Stat(p)
	found := err == nil
	if found {
		if rmErr := os.Remove(p); rmErr != nil {
			log.Printf("openfortitray: could not remove resume marker %s: %v", p, rmErr)
		}
	}
	return found
}

// selfHealThenConnect runs the startup sequence off the UI thread: it first reaps
// any tunnel orphaned by a previous unclean exit (so a prior crash's root
// openconnect and its FortiGate session are cleared BEFORE we mint a new cookie),
// then, only if autostart is enabled, connects. The reap runs to completion first
// — ordering matters, because minting a cookie while the old session is still held
// is exactly what triggers the "Cookie was rejected" loop. reap is bounded by
// startupReapWait and best-effort; a failure is logged, never fatal. connect is
// marshalled onto the UI goroutine by the caller (a.Connect touches the UI).
func (a *app) selfHealThenConnect(reap func(ctx context.Context) error, autostart bool, connect func()) {
	ctx, cancel := context.WithTimeout(context.Background(), startupReapWait)
	defer cancel()
	if err := reap(ctx); err != nil {
		log.Printf("openfortitray: startup reap of a stale tunnel failed (best-effort): %v", err)
	}
	if autostart {
		connect()
	}
}

// SetAutostart toggles the login item and the saved preference together. If the
// preference cannot be saved the login item is rolled back to the state it had
// before the click, so the OS never disagrees with what the menu shows; a failed
// rollback is logged as a real divergence.
func (a *app) SetAutostart(on bool) error {
	was := autostart.IsEnabled()
	if err := setLoginItem(on); err != nil {
		log.Printf("autostart: %v", err)
		return err
	}
	a.cfg.Autostart = on
	if err := a.cfg.Save(a.cfgDir); err != nil {
		log.Printf("config save: %v", err)
		a.cfg.Autostart = was
		if rb := setLoginItem(was); rb != nil {
			log.Printf("autostart: rollback to enabled=%v failed, login item and config now disagree: %v", was, rb)
		}
		return err
	}
	return nil
}

// fyneRootConfigDir mirrors fyne's own internal/app.rootConfigDir (v2.8): fyne
// stores preferences.json under <root>/<appID>/. It is reimplemented here rather
// than imported because fyne's is in an internal package. Kept in step with the
// fyne version pinned in go.mod.
func fyneRootConfigDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Preferences", "fyne")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "fyne")
	default:
		base, _ := os.UserConfigDir()
		return filepath.Join(base, "fyne")
	}
}

// sanitizeFynePreferences removes fyne's preferences.json for appID when it is
// empty or not valid JSON, so fyne's loader sees a missing (clean, empty) store
// instead of logging "Fyne Preferences load error: EOF". A file that parses as
// JSON is left untouched. Best-effort: every error is logged and swallowed —
// this is cosmetic, never a reason to fail startup.
func sanitizeFynePreferences(appID string) {
	path := filepath.Join(fyneRootConfigDir(), appID, "preferences.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // missing (the common case) or unreadable: fyne handles missing itself
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && json.Valid(trimmed) {
		return // real preferences: leave them alone
	}
	if err := os.Remove(path); err != nil {
		log.Printf("openfortitray: could not clear corrupt fyne preferences %s: %v", path, err)
		return
	}
	log.Printf("openfortitray: cleared corrupt/empty fyne preferences %s (%d bytes)", path, len(data))
}

// setLoginItem installs or removes the per-user login item for this executable.
func setLoginItem(on bool) error {
	if !on {
		return autostart.Disable()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return autostart.Enable(exe)
}

// authTimeout bounds a single SAML login attempt (the user has to click
// through an external browser).
const authTimeout = 5 * time.Minute

// shutdownWait caps how long we wait for the backend to tear the tunnel down on
// quit. os/exec runs the runner's Cancel to completion before starting its
// WaitDelay timer, so the worst case is the sum of the two: two privileged-helper
// stop attempts (2*10s) followed by the 12s backstop, i.e. 32s. It MUST cover
// that sum so a clean quit never SIGKILLs openconnect before it has sent its
// logout to the FortiGate — a session left holding open is exactly what rejects
// the next run's first cookie. Quitting a few seconds slower is a better trade
// than quitting fast and leaving the machine on the VPN with a root openconnect
// nobody can signal. The normal path returns in well under a second.
const shutdownWait = 35 * time.Second

// startupReapWait bounds the best-effort reap of an orphaned tunnel at launch
// (selfHealThenConnect → tunnel.ReapStale). The helper's "stop" waits up to a few
// seconds for a live openconnect to exit and returns at once when there is
// nothing to reap, so a clean start pays almost none of this; the cap only guards
// against a wedged privileged call holding up connect-on-launch.
const startupReapWait = 15 * time.Second

// cookieRetryInterval/cookieRetryWindow bound cookieGetWithRetry: how long
// authenticate waits for a busy OS secret store (credstore.ErrBusy) before
// giving up on the stored cookie and falling back to SAML. A macOS login-item
// launch can race the login keychain's automatic unlock by a couple of
// seconds; this window is chosen to comfortably outlast that race without
// making a genuinely-empty store noticeably slower to fall back.
const (
	cookieRetryInterval = 500 * time.Millisecond
	cookieRetryWindow   = 5 * time.Second
)

func main() {
	// Windows ships a bundled Mesa software OpenGL (the app-dir opengl32.dll
	// shadows the system one), whose default gallium driver is llvmpipe. llvmpipe
	// JITs with LLVM and uses CPU vector instructions, and on some GPU-less hosts
	// (locked-down Cloud PCs / RDP) that hard-crashes on the first window draw —
	// the tray survives (no GL surface) but opening any window kills the process
	// with no WER/crash record. Force the pure-C softpipe driver, which renders
	// this light UI fine and does not crash there. Must be set before any GL call
	// (i.e. before fyne creates its driver). No-op off Windows.
	if runtime.GOOS == "windows" {
		_ = os.Setenv("GALLIUM_DRIVER", "softpipe")
	}

	cfgDir, err := config.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(cfgDir)
	if err != nil {
		log.Fatal(err)
	}
	logPath := filepath.Join(cfgDir, "openfortitray.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
		// A -H=windowsgui build has no console, so a Go runtime panic or a cgo/GL
		// crash (which write to stderr / fd 2) would vanish. Point the process's
		// stderr at the log file so those land there too. redirectStderr is a no-op
		// off Windows, where stderr already works.
		os.Stderr = f
		redirectStderr(f)
		defer f.Close()
	}
	// Startup breadcrumb + build stamp. Pair it with the "run loop returned" line
	// below: if the log ends after this but before that, the process died in
	// startup/run rather than exiting cleanly — the key signal for diagnosing a
	// silent GUI crash. A main-goroutine panic is captured here with its stack;
	// a C-level (glfw/Mesa) crash lands via the stderr redirect above.
	log.Printf("openfortitray %s starting (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in main: %v\n%s", r, debug.Stack())
			os.Exit(2)
		}
	}()

	// Single-instance guard, acquired BEFORE any SAML/connect. Without it a second
	// launch runs its own SAML login and connect, and the two instances fight over
	// the one per-user FortiGate session — the dual-instance contention behind the
	// "listen tcp 127.0.0.1:8020: bind: address already in use" and the ensuing
	// cookie-rejected storm from the real logs. flock releases automatically when
	// the process dies, so a crash leaves no stale lock for the next launch to
	// clear. A second instance exits cleanly (status 0): the first is already up.
	lockPath := filepath.Join(cfgDir, "openfortitray.lock")
	lock, err := acquireInstanceLock(lockPath)
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			log.Printf("openfortitray: %v; exiting (only one instance may run)", err)
			os.Exit(0)
		}
		log.Fatalf("openfortitray: cannot acquire single-instance lock %s: %v", lockPath, err)
	}
	defer lock.release()

	if prof := cfg.Active(); prof.Gateway == "" {
		log.Printf("openfortitray: starting, no gateway configured — Connect opens Settings to add one")
	} else {
		log.Printf("openfortitray: starting, gateway %s", prof.GatewayURL())
	}

	events := make(chan tunnel.Event, 16)
	a := &app{
		cfg:     cfg,
		cfgDir:  cfgDir,
		events:  events,
		logPath: logPath,
		// The credstore seam: real platform-native store in production, an
		// in-memory fake in tests.
		cookieGet:    credstore.Get,
		cookieSet:    credstore.Set,
		cookieDelete: credstore.Delete,
		samlAuth:     defaultSAMLAuth,

		cookieRetryInterval: cookieRetryInterval,
		cookieRetryWindow:   cookieRetryWindow,
	}

	// The auth/run funcs read a.snapshot() rather than a value captured at
	// startup, so a Connect that follows a settings Save (Save & Reconnect) dials
	// the freshly edited active profile. Connect refreshes the snapshot on the UI
	// goroutine before starting the supervisor; these run on its goroutines.
	authFn := tunnel.AuthFunc(a.authenticate)

	// macOS/Linux go through the root-owned helper installed by
	// scripts/install.sh (see internal/tunnel.Options); on Windows the app is
	// already elevated and runs openconnect itself.
	runFn := loggedRun(func(ctx context.Context, cookie string, connected func(ip string)) error {
		tp := a.snapshot()
		// Scoped resolvers (/etc/resolver/<domain>) are a macOS mechanism. On any
		// other OS leave SplitDNS empty so the tunnel installs nothing rather than
		// writing files that do nothing.
		//
		// TODO(linux-splitdns): Linux split-DNS is a systemd-resolved job
		// (resolvectl domain <iface> ~<domain> + a per-link DNS keyed off the
		// tunnel interface), not /etc/resolver. It is not automated yet; see
		// internal/dns.Discover's non-darwin stub.
		splitDNS := tp.prof.SplitDNS
		if runtime.GOOS != "darwin" {
			splitDNS = nil
		}
		run := tunnel.RunOpenconnect(tunnel.Options{
			Gateway:         fmt.Sprintf("%s:%d", tp.prof.Gateway, tp.prof.Port),
			OpenconnectPath: tp.openconnectPath,
			HelperPath:      tp.helperPath,
			UseSudo:         runtime.GOOS != "windows",
			// Where the direct (Windows) path writes the short-lived openconnect
			// config holding the cookie: the app's own config directory, not a
			// shared temp root.
			ConfDir:  filepath.Join(a.cfgDir, "run"),
			SplitDNS: splitDNS,
			// Tunnel-shaping toggles from the active profile. They reach
			// openconnect on BOTH paths: the direct (Windows) path and the
			// privileged helper path, which validates each flag against an exact
			// allowlist before openconnect sees it (Task 24; see tunnel.Options
			// and scripts/openfortitray-tunnel).
			DTLS: tp.prof.DTLS,
			// End the session on the gateway when the tunnel goes down. openconnect
			// never does this for Fortinet, so without it the FortiGate holds the
			// session until its own timeout and refuses every reconnect in between
			// (one SSL-VPN session per user) — measured at 3.5 minutes of refusals
			// after a clean disconnect. Best-effort and bounded; the stored cookie is
			// dropped too, since a logged-out session's cookie is dead.
			Logout: func(cookie string) {
				ctx, cancel := context.WithTimeout(context.Background(), auth.LogoutTimeout)
				defer cancel()
				if err := auth.Logout(ctx, auth.LogoutClient(), tp.prof.GatewayURL(), cookie); err != nil {
					// Not fatal, and not unexpected: this gateway often answers with a
					// redirect to the IdP, meaning it no longer considers the request
					// authenticated. Then the session is released on the gateway's own
					// schedule — measured at a median of 25s — and the connect path waits
					// that out quietly.
					log.Printf("tunnel: gateway did not confirm the logout (%v); "+
						"the session will be released on the gateway's own schedule", err)
				} else {
					log.Printf("tunnel: session ended on the gateway")
				}
				if tp.prof.Gateway != "" {
					if err := a.cookieDelete(cookieKey(tp.prof.Gateway)); err != nil {
						log.Printf("tunnel: could not drop the logged-out session cookie: %v", err)
					}
				}
			},
			DualStack:      tp.prof.DualStack,
			ServerCertMode: string(tp.prof.ServerCert.Mode),
			ServerCertPin:  tp.prof.ServerCert.Pin,
		})
		return run(ctx, cookie, connected)
	})

	a.sup = tunnel.New(authFn, runFn, events)

	// fyne owns the main thread: NewWithID (not bare New) so tray/preferences
	// plumbing has a stable app identity. The tray must be built before Run.
	//
	// Migrations["fyneDo"] declares that this app already marshals every
	// cross-goroutine UI mutation through fyne.Do (the event pump does; menu
	// Actions run on the UI goroutine). Without it fyne v2.8 logs a standing
	// "not migrated to the fyne.Do threading model" advisory at Run(). The
	// thread-safety checks themselves stay active.
	fyneapp.SetMetadata(fyne.AppMetadata{
		ID:         "io.github.savvaskoualis.openfortitray",
		Name:       "OpenFortiTray",
		Migrations: map[string]bool{"fyneDo": true},
	})
	// A previous unclean write can leave fyne's preferences.json empty or corrupt,
	// which makes fyne log a scary "Fyne Preferences load error: EOF" at startup.
	// Clear it first (the app keeps its real settings in internal/config, not in
	// fyne preferences, so this loses nothing) so fyne sees a clean, empty store.
	sanitizeFynePreferences("io.github.savvaskoualis.openfortitray")
	a.fyneApp = fyneapp.NewWithID("io.github.savvaskoualis.openfortitray")
	// The app theme, installed before any window is built so nothing is ever laid
	// out against the default palette and then re-laid out. It tracks the OS
	// light/dark setting: fyne resolves the variant and hands it to Color.
	a.fyneApp.Settings().SetTheme(uitheme.New())
	ctrl, err := tray.Setup(a.fyneApp, a)
	if err != nil {
		log.Fatal(err)
	}
	a.tray = ctrl
	log.Print("tray: system tray menu installed")

	// Desktop notifications for the transitions worth interrupting for (see
	// notifyFor). Wired only now that the fyne app exists; before this the pump
	// would have had nothing to send through, and a.notify == nil is a no-op.
	a.notify = func(title, body string) {
		a.fyneApp.SendNotification(fyne.NewNotification(title, body))
	}

	// Best-effort menu-bar tooltip. fyne has no tooltip API, so this reaches the
	// systray singleton fyne drives. It must run after the tray is live: fyne
	// starts the tray during Run and then fires OnStarted (on the UI goroutine),
	// which is the first moment the native status item exists. tray.SetTooltip is
	// guarded, so a not-ready tray or unsupported platform is a silent no-op.
	a.fyneApp.Lifecycle().SetOnStarted(func() {
		log.Print("fyne lifecycle: OnStarted (tray live)")
		// Re-assert the tray icon + menu now that the native systray exists. On
		// Windows the initial set in tray.Setup (before the run loop) logs "tray not
		// ready yet" and no icon appears; setting it again here makes it stick.
		a.tray.ReassertTray()
		log.Print("tray: re-asserted icon+menu after OnStarted")
		tray.SetTooltip("OpenFortiTray")
		// Assert the Dock-visible (Regular) activation policy. fyne/glfw sets its
		// own policy while initializing NSApp during Run, so the policy the app
		// wants has to be set AFTER that — OnStarted fires on the UI/main goroutine
		// once NSApp exists, which is both late enough and on the right thread.
		// No-op off darwin.
		setDockActivationPolicy()
		// Give the Dock icon an effect. fyne does not implement AppKit's reopen
		// delegate method, so without this the icon is inert: clicking it does
		// nothing at all, which is worse than having no icon.
		//
		// The FIRST activation is ignored on purpose. Launching the app activates it,
		// and a window appearing unasked at every login is exactly the behaviour a
		// tray app should not have. Every activation after that is a deliberate
		// "bring this up" — a Dock click or a Cmd-Tab — and shows the window.
		firstActivation := true
		watchDockActivation(func() {
			if firstActivation {
				firstActivation = false
				log.Print("dock: first activation (launch) — leaving the window hidden")
				return
			}
			log.Print("dock: activated — showing the status window")
			a.ShowStatus()
		})
		a.startUptimeTicker()
	})
	// OnStopped fires when fyne itself tears the run loop down. If this appears in
	// the log (rather than the "run loop returned" line, or nothing), the app is
	// being quit by fyne — e.g. a tray-only app the driver did not keep alive —
	// not crashing. The distinction drives the fix.
	a.fyneApp.Lifecycle().SetOnStopped(func() {
		log.Print("fyne lifecycle: OnStopped (fyne is quitting the run loop)")
	})

	// ONE window, built once and left hidden. It is never ShowAndRun'd, so it cannot
	// be the master window whose close quits the app; the shell intercepts its close
	// to Hide.
	//
	// Status and Settings were two separate windows: two things to find, two to
	// arrange, and — once the app grew a Dock icon — an ambiguous answer to "bring
	// this app up". The controllers still take the window, because dialogs and focus
	// need one, but they no longer decide what it contains or when it appears.
	win := a.fyneApp.NewWindow("OpenFortiTray")
	a.win = win
	a.settings = settings.New(a, win)
	a.status = status.New(a, win)

	a.shell = shell.New(win, shell.Parts{
		Status:     a.status.Content(),
		Connection: a.settings.ConnectionContent(),
		Advanced:   a.settings.AdvancedContent(),
		ProfileBar: a.settings.ProfileBar(),
		Banner:     a.settings.Banner(),
		Footer:     a.settings.Footer(),
	})
	// Settings asks the shell to navigate when a refused Connect points at a field.
	a.settings.SetNavigator(func(tab string) {
		if tab == settings.TabAdvanced {
			a.shell.Reveal(shell.SectionAdvanced)
			return
		}
		a.shell.Reveal(shell.SectionConnection)
	})
	// Revealing the activity history needs a taller window; the shell owns the size.
	a.status.OnHeightRequest = a.shell.RequestHeight

	// Route a refused Connect (invalid active profile) to the settings window,
	// which opens on the offending field with a banner naming the fix.
	a.onConnectIssue = func(i *settings.Issue) {
		log.Print("onConnectIssue: showing settings window")
		a.settings.ShowIssue(i)
		log.Print("onConnectIssue: settings window shown")
	}
	// Wire the first-run privileged-helper install (macOS only; a no-op elsewhere,
	// where the manual scripts/install.sh path is unchanged). Must be after a.win
	// and a.settings are set — the bootstrap dialogs parent on a.win.
	a.installBootstrapHooks()

	// The one event pump. Started before Run so events emitted by the
	// connect-on-launch below queue onto fyne's (unbounded) main-loop queue and
	// render as soon as Run starts.
	go a.pump()

	// Signal-driven exit. launchd's stop (SIGTERM), Ctrl-C (SIGINT), a hangup
	// (SIGHUP) and a plain `pkill` all route to the SAME graceful shutdown the
	// tray Quit uses, so the tunnel is torn down through the root helper's "stop"
	// instead of the process leaving a root openconnect orphaned. buffered so a
	// signal is never dropped before the handler is scheduled.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go a.watchSignals(sigs, func() { fyne.Do(a.fyneApp.Quit) })

	// Sleep/wake-driven reconnect: a laptop resuming from sleep is the one drop
	// openconnect's own dead-peer detection is slowest to notice (this gateway
	// disables openconnect's self-managed reconnect entirely — see
	// onSystemWake). watchSystemSleep is a native per-OS hook (NSWorkspace on
	// macOS, PowerRegisterSuspendResumeNotification on Windows, logind's
	// PrepareForSleep over D-Bus on Linux); onSystemWake decides whether to act.
	watchSystemSleep(a.onSystemWake)

	// Startup self-heal, then connect-on-launch — off the UI thread and in that
	// order. Reaping a tunnel orphaned by a previous unclean exit BEFORE minting a
	// new cookie clears the stale FortiGate session that would otherwise reject
	// the cookie in a loop. On the direct path (Windows) ReapStale is a no-op.
	// The connect is marshalled back onto the UI goroutine (a.Connect touches the
	// settings window when the active profile is unconfigured); it queues onto
	// fyne's main-loop queue and runs as soon as Run starts.
	// resumed is a SEPARATE question from cfg.Autostart: it is set for exactly one
	// launch, right after an update restart that tore down a tunnel which was
	// actually connected — see consumeResumeMarker. Without it, a user who
	// disabled the login item (cfg.Autostart) lost the promised automatic
	// reconnect after every update.
	resumed := consumeResumeMarker(cfgDir)
	if resumed {
		log.Print("openfortitray: resuming the VPN session that was up before this update restart")
	}
	reapOpts := tunnel.Options{HelperPath: cfg.HelperPath, UseSudo: runtime.GOOS != "windows"}
	go a.selfHealThenConnect(reapOpts.ReapStale, cfg.Autostart || resumed, func() { fyne.Do(a.Connect) })

	// Background update checker: polls GitHub for a newer release and, if found,
	// surfaces a one-click "Update … & Restart" item on the tray. Fully best-effort
	// and off the UI thread; a bare/untagged build reports version "dev", which the
	// checker treats as never-newer, so local runs never prompt.
	go a.startUpdateChecker(context.Background())

	// Run blocks the main goroutine until a.fyneApp.Quit(), which the tray's Quit
	// item and the signal handler both drive only after the tunnel has been torn
	// down (see app.shutdown). A tray-only fyne app (no window ever shown) stays
	// alive here and exits cleanly on Quit — verified against fyne v2.8's glfw
	// run loop.
	log.Print("entering fyne run loop")
	a.fyneApp.Run()
	log.Print("fyne run loop returned; waiting for the tunnel teardown")
	// fyne quits the run loop from its own signal handler, so arriving here does
	// NOT mean the tunnel is down. Block until it is (see awaitShutdown) —
	// otherwise the process exits mid-teardown and leaks the server-side session.
	a.awaitShutdown()
	log.Print("app exiting")
}

// loggedRun wraps runFn so every backend exit lands in the log file.
func loggedRun(run tunnel.RunFunc) tunnel.RunFunc {
	return func(ctx context.Context, cookie string, connected func(ip string)) error {
		log.Printf("tunnel: starting openconnect")
		err := run(ctx, cookie, func(ip string) {
			log.Printf("tunnel: connected as %s", ip)
			connected(ip)
		})
		log.Printf("tunnel: exited: %v", err)
		return err
	}
}
