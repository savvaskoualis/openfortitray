// Command hyp-vpn is the Hyperio VPN tray application.
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

	"github.com/hyperiosoftware/hyp-vpn/internal/auth"
	"github.com/hyperiosoftware/hyp-vpn/internal/autostart"
	"github.com/hyperiosoftware/hyp-vpn/internal/config"
	"github.com/hyperiosoftware/hyp-vpn/internal/tray"
	"github.com/hyperiosoftware/hyp-vpn/internal/tunnel"
)

// app adapts the packages to tray.App; it holds no logic of its own.
type app struct {
	cfg     *config.Config
	cfgDir  string
	sup     *tunnel.Supervisor
	events  chan tunnel.Event
	logPath string
}

func (a *app) Connect()                    { a.sup.Connect() }
func (a *app) Disconnect()                 { a.sup.Disconnect() }
func (a *app) AutostartEnabled() bool      { return autostart.IsEnabled() }
func (a *app) LogPath() string             { return a.logPath }
func (a *app) Events() <-chan tunnel.Event { return a.events }

func (a *app) SetAutostart(on bool) error {
	var err error
	if on {
		exe, e := os.Executable()
		if e != nil {
			log.Printf("autostart: %v", e)
			return e
		}
		err = autostart.Enable(exe)
	} else {
		err = autostart.Disable()
	}
	if err != nil {
		log.Printf("autostart: %v", err)
		return err
	}
	a.cfg.Autostart = on
	if err := a.cfg.Save(a.cfgDir); err != nil {
		log.Printf("config save: %v", err)
		return err
	}
	return nil
}

// authTimeout bounds a single SAML login attempt (the user has to click
// through an external browser).
const authTimeout = 5 * time.Minute

// shutdownGrace lets openconnect handle SIGINT and restore routing before the
// process exits.
const shutdownGrace = 3 * time.Second

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
	logPath := filepath.Join(cfgDir, "hyp-vpn.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	log.Printf("hyp-vpn: starting, gateway %s", cfg.GatewayURL())

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

	useSudo := runtime.GOOS != "windows"
	runFn := loggedRun(tunnel.RunOpenconnect(cfg.OpenconnectPath,
		fmt.Sprintf("%s:%d", cfg.Gateway, cfg.Port), useSudo))

	events := make(chan tunnel.Event, 16)
	a := &app{
		cfg:     cfg,
		cfgDir:  cfgDir,
		sup:     tunnel.New(authFn, runFn, events),
		events:  events,
		logPath: logPath,
	}

	if cfg.Autostart {
		a.sup.Connect() // launch happens at login, so connect right away
	}
	tray.Run(a)

	// tray.Run returned: the user quit. Give the backend a moment to tear the
	// tunnel down before the process (and its log file) go away.
	a.sup.Disconnect()
	time.Sleep(shutdownGrace)
	log.Printf("hyp-vpn: exiting")
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
