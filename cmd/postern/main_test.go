package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/savvaskoualis/postern/internal/config"
	"github.com/savvaskoualis/postern/internal/tunnel"
)

// newTestApp builds an app whose supervisor records whether it was ever asked to
// authenticate — i.e. whether a connection attempt actually started.
func newTestApp(t *testing.T, gateway, cfgDir string) (*app, chan struct{}) {
	t.Helper()
	authCalled := make(chan struct{}, 1)
	authFn := func(ctx context.Context) (string, error) {
		select {
		case authCalled <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	runFn := func(ctx context.Context, cookie string, connected func(ip string)) error {
		<-ctx.Done()
		return ctx.Err()
	}
	events := make(chan tunnel.Event, 16)
	a := &app{
		cfg:    &config.Config{Gateway: gateway, Port: 10443},
		cfgDir: cfgDir,
		sup:    tunnel.New(authFn, runFn, events),
		events: events,
	}
	t.Cleanup(func() {
		a.sup.Disconnect()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.sup.Wait(ctx)
	})
	return a, authCalled
}

// An unconfigured gateway must not reach openconnect: ":10443" would open a SAML
// browser window against nothing and report an opaque connection failure, so the
// missing setting is reported instead — and it has to be the terminal Error
// state, because that is what leaves Connect clickable in the menu.
func TestConnectWithoutGatewayReportsMissingSetting(t *testing.T) {
	cfgDir := t.TempDir()
	a, authCalled := newTestApp(t, "", cfgDir)

	a.Connect()

	select {
	case e := <-a.Events():
		if e.State != tunnel.Error {
			t.Fatalf("state = %v, want Error (terminal, so Connect stays clickable)", e.State)
		}
		if !strings.Contains(e.Detail, "gateway not set") {
			t.Errorf("detail = %q, want it to say the gateway is not set", e.Detail)
		}
		if !strings.Contains(e.Detail, "config.json") {
			t.Errorf("detail = %q, want it to name the file to edit (config.json)", e.Detail)
		}
		// The tray clips the status line at 60 runes, so a detail longer than that
		// reaches the user truncated. The absolute config path does not fit; it
		// lives in the log line instead.
		if n := len([]rune(e.Detail)); n > 60 {
			t.Errorf("detail is %d runes (%q); the tray clips at 60", n, e.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event emitted for a missing gateway")
	}

	select {
	case <-authCalled:
		t.Error("authentication started despite no gateway being configured")
	case <-time.After(100 * time.Millisecond):
	}
}

// The guard must not swallow real connects.
func TestConnectWithGatewayStartsSupervisor(t *testing.T) {
	a, authCalled := newTestApp(t, "vpn.example.com", t.TempDir())

	a.Connect()

	select {
	case <-authCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor never started authenticating")
	}
}
