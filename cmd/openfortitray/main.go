// Command openfortitray is the OpenFortiTray tray application.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"

	"github.com/savvaskoualis/openfortitray/internal/auth"
	"github.com/savvaskoualis/openfortitray/internal/autostart"
	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/tray"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
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
		openconnectPath: a.cfg.OpenconnectPath,
		helperPath:      a.cfg.HelperPath,
	})
	a.sup.Connect()
}

func (a *app) Disconnect()            { a.sup.Disconnect() }
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
	if a.settings != nil {
		a.settings.Show()
	}
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
	*a.cfg = *c
	return nil
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
		a.quitting.Store(true)
		go func() {
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

func main() {
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
		defer f.Close()
	}

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
	}

	// The auth/run funcs read a.snapshot() rather than a value captured at
	// startup, so a Connect that follows a settings Save (Save & Reconnect) dials
	// the freshly edited active profile. Connect refreshes the snapshot on the UI
	// goroutine before starting the supervisor; these run on its goroutines.
	authFn := tunnel.AuthFunc(func(ctx context.Context) (string, error) {
		prof := a.snapshot().prof
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
	})

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
			SplitDNS:        splitDNS,
			// Tunnel-shaping toggles from the active profile. They reach
			// openconnect on BOTH paths: the direct (Windows) path and the
			// privileged helper path, which validates each flag against an exact
			// allowlist before openconnect sees it (Task 24; see tunnel.Options
			// and scripts/openfortitray-tunnel).
			DTLS:           tp.prof.DTLS,
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
	ctrl, err := tray.Setup(a.fyneApp, a)
	if err != nil {
		log.Fatal(err)
	}
	a.tray = ctrl

	// Best-effort menu-bar tooltip. fyne has no tooltip API, so this reaches the
	// systray singleton fyne drives. It must run after the tray is live: fyne
	// starts the tray during Run and then fires OnStarted (on the UI goroutine),
	// which is the first moment the native status item exists. tray.SetTooltip is
	// guarded, so a not-ready tray or unsupported platform is a silent no-op.
	a.fyneApp.Lifecycle().SetOnStarted(func() {
		tray.SetTooltip("OpenFortiTray")
		// fyne/glfw promotes the process to a Regular (Dock-visible) app when it
		// initializes NSApp during Run, overriding Info.plist LSUIElement=1. Undo
		// that here: OnStarted fires on the UI/main goroutine after NSApp exists,
		// so setting the Accessory activation policy now hides the Dock icon
		// without racing fyne. No-op on non-darwin.
		setAccessoryActivationPolicy()
	})

	// Build the settings window once, hidden. It is never ShowAndRun'd, so it
	// cannot be the master window whose close quits the app; its close button is
	// intercepted to Hide (see settings.build). The tray's Settings… item shows
	// this same reused window.
	win := a.fyneApp.NewWindow("OpenFortiTray — Settings")
	a.win = win
	a.settings = settings.New(a, win)
	// Route a refused Connect (invalid active profile) to the settings window,
	// which opens on the offending field with a banner naming the fix.
	a.onConnectIssue = a.settings.ShowIssue
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

	// Startup self-heal, then connect-on-launch — off the UI thread and in that
	// order. Reaping a tunnel orphaned by a previous unclean exit BEFORE minting a
	// new cookie clears the stale FortiGate session that would otherwise reject
	// the cookie in a loop. On the direct path (Windows) ReapStale is a no-op.
	// The connect is marshalled back onto the UI goroutine (a.Connect touches the
	// settings window when the active profile is unconfigured); it queues onto
	// fyne's main-loop queue and runs as soon as Run starts.
	reapOpts := tunnel.Options{HelperPath: cfg.HelperPath, UseSudo: runtime.GOOS != "windows"}
	go a.selfHealThenConnect(reapOpts.ReapStale, cfg.Autostart, func() { fyne.Do(a.Connect) })

	// Run blocks the main goroutine until a.fyneApp.Quit(), which the tray's Quit
	// item and the signal handler both drive only after the tunnel has been torn
	// down (see app.shutdown). A tray-only fyne app (no window ever shown) stays
	// alive here and exits cleanly on Quit — verified against fyne v2.8's glfw
	// run loop.
	a.fyneApp.Run()
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
