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
	"time"

	"github.com/savvaskoualis/openfortitray/internal/auth"
	"github.com/savvaskoualis/openfortitray/internal/autostart"
	"github.com/savvaskoualis/openfortitray/internal/config"
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
	if a.cfg.Gateway == "" {
		log.Printf("connect refused: gateway not set — edit %s",
			filepath.Join(a.cfgDir, "config.json"))
		a.emit(tunnel.Event{State: tunnel.Error, Detail: "gateway not set — see config.json"})
		return
	}
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

func (a *app) Disconnect()                 { a.sup.Disconnect() }
func (a *app) AutostartEnabled() bool      { return autostart.IsEnabled() }
func (a *app) LogPath() string             { return a.logPath }
func (a *app) Events() <-chan tunnel.Event { return a.events }

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
	if cfg.Gateway == "" {
		log.Printf("openfortitray: starting, no gateway configured in %s",
			filepath.Join(cfgDir, "config.json"))
	} else {
		log.Printf("openfortitray: starting, gateway %s", cfg.GatewayURL())
	}

	authr := &auth.Authenticator{
		GatewayURL: cfg.GatewayURL(),
		ListenPort: cfg.SAMLPort,
		Client:     &http.Client{Timeout: 30 * time.Second},
	}
	authFn := tunnel.AuthFunc(func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, authTimeout)
		defer cancel()
		log.Printf("auth: starting SAML login on 127.0.0.1:%d", cfg.SAMLPort)
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
	runFn := loggedRun(tunnel.RunOpenconnect(tunnel.Options{
		Gateway:         fmt.Sprintf("%s:%d", cfg.Gateway, cfg.Port),
		OpenconnectPath: cfg.OpenconnectPath,
		HelperPath:      cfg.HelperPath,
		UseSudo:         runtime.GOOS != "windows",
	}))

	events := make(chan tunnel.Event, 16)
	a := &app{
		cfg:     cfg,
		cfgDir:  cfgDir,
		sup:     tunnel.New(authFn, runFn, events),
		events:  events,
		logPath: logPath,
	}

	if cfg.Autostart {
		a.Connect() // launch happens at login, so connect right away
	}
	tray.Run(a)

	// tray.Run returned: the user quit. Block until the backend has torn the
	// tunnel down (routing restored) before the process and its log file go away.
	a.sup.Disconnect()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	a.sup.Wait(ctx)
	if ctx.Err() != nil {
		log.Printf("openfortitray: backend did not stop within %s", shutdownWait)
	}
	log.Printf("openfortitray: exiting")
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
