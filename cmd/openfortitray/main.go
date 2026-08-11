// Command openfortitray is the OpenFortiTray tray application.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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

// app adapts the packages to tray.App; it holds no logic of its own.
type app struct {
	cfg     *config.Config
	cfgDir  string
	sup     *tunnel.Supervisor
	events  chan tunnel.Event
	logPath string

	fyneApp  fyne.App
	tray     *tray.Controller
	settings *settings.Controller
	// quitting stops the event pump touching a tearing-down UI. It is set once,
	// on the UI goroutine, at the start of Quit; the pump reads it and, once set,
	// drains events without calling fyne.Do — so no fyne.Do is ever queued
	// against a UI that a.fyneApp.Quit() is about to destroy.
	quitting atomic.Bool

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

// Connect starts the tunnel, unless no gateway is configured. The gateway has no
// built-in default (it is deployment-specific), so a fresh install would
// otherwise hand openconnect a bare ":10443" — the SAML browser window would
// open against a nonexistent host and the failure would surface as an opaque
// connection error. Reporting the missing setting instead, as the terminal Error
// state, tells the user the one thing they have to do.
//
// The menu text names the file but not its directory, because tray.short() clips
// the detail at 60 runes and the full path ("~/Library/Application
// Support/openfortitray/config.json") does not fit — the user would see the sentence
// truncated mid-path, which is worse than no path at all. The log line below
// carries the absolute path for anyone who needs it.
func (a *app) Connect() {
	prof := *a.cfg.Active()
	if prof.Gateway == "" {
		log.Printf("connect refused: gateway not set — edit %s",
			filepath.Join(a.cfgDir, "config.json"))
		a.emit(tunnel.Event{State: tunnel.Error, Detail: "gateway not set — see config.json"})
		return
	}
	// Snapshot the active profile the tunnel will dial, so a Connect that follows
	// a settings Save picks up the edited profile rather than the startup one.
	a.setSnapshot(tunnelParams{
		prof:            prof,
		openconnectPath: a.cfg.OpenconnectPath,
		helperPath:      a.cfg.HelperPath,
	})
	a.sup.Connect()
}

// emit delivers an event the supervisor did not produce, without blocking on a
// slow UI (the same drop-on-full rule the supervisor uses).
func (a *app) emit(e tunnel.Event) {
	select {
	case a.events <- e:
	default:
	}
}

func (a *app) Disconnect()            { a.sup.Disconnect() }
func (a *app) AutostartEnabled() bool { return autostart.IsEnabled() }
func (a *app) LogPath() string        { return a.logPath }

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
		})
	}
}

// Quit is invoked from the tray's Quit item on the UI goroutine. It tears the
// tunnel down before the process leaves, exactly as the old post-systray.Run
// block did, but off the UI goroutine: sup.Wait can block for shutdownWait, and
// blocking the UI goroutine would freeze the menu bar. Once the tunnel is down
// it marshals a.fyneApp.Quit() back onto the UI goroutine (fyne's own signal
// handler quits the same way), which unblocks a.fyneApp.Run() in main.
func (a *app) Quit() {
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
		fyne.Do(a.fyneApp.Quit)
	}()
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
// stop attempts (2*8s) followed by the 12s backstop, i.e. 28s. Quitting a few
// seconds slower is a better trade than quitting fast and leaving the machine on
// the VPN with a root openconnect nobody can signal. The normal path returns in
// well under a second.
const shutdownWait = 30 * time.Second

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
	if prof := cfg.Active(); prof.Gateway == "" {
		log.Printf("openfortitray: starting, no gateway configured in %s",
			filepath.Join(cfgDir, "config.json"))
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
		run := tunnel.RunOpenconnect(tunnel.Options{
			Gateway:         fmt.Sprintf("%s:%d", tp.prof.Gateway, tp.prof.Port),
			OpenconnectPath: tp.openconnectPath,
			HelperPath:      tp.helperPath,
			UseSudo:         runtime.GOOS != "windows",
			// Tunnel-shaping toggles from the active profile. They reach
			// openconnect on the direct (Windows) path; the privileged helper
			// path does not yet carry them (see tunnel.Options).
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
	a.fyneApp.Lifecycle().SetOnStarted(func() { tray.SetTooltip("OpenFortiTray") })

	// Build the settings window once, hidden. It is never ShowAndRun'd, so it
	// cannot be the master window whose close quits the app; its close button is
	// intercepted to Hide (see settings.build). The tray's Settings… item shows
	// this same reused window.
	win := a.fyneApp.NewWindow("OpenFortiTray — Settings")
	a.settings = settings.New(a, win)

	// The one event pump. Started before Run so events emitted by the
	// connect-on-launch below queue onto fyne's (unbounded) main-loop queue and
	// render as soon as Run starts.
	go a.pump()

	if cfg.Autostart {
		a.Connect() // launch happens at login, so connect right away
	}

	// Run blocks the main goroutine until a.fyneApp.Quit(), which the tray's
	// Quit item drives only after the tunnel has been torn down (see app.Quit).
	// A tray-only fyne app (no window ever shown) stays alive here and exits
	// cleanly on Quit — verified against fyne v2.8's glfw run loop.
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
