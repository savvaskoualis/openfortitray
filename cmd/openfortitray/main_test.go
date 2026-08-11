package main

import (
	"context"
	"testing"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
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
		cfg: &config.Config{
			ActiveProfile: "Default",
			Profiles:      []config.Profile{{Name: "Default", Gateway: gateway, Port: 10443}},
		},
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
// browser window against nothing and report an opaque connection failure. Rather
// than start a doomed tunnel and flash a red Error, Connect routes the user to
// the settings window — the right tab and field — and does not dial.
func TestConnectWithIssueRoutesToSettingsAndDoesNotDial(t *testing.T) {
	a, authCalled := newTestApp(t, "", t.TempDir()) // empty gateway → blocking issue

	var got *settings.Issue
	a.onConnectIssue = func(i *settings.Issue) { got = i }

	a.Connect()

	if got == nil {
		t.Fatal("Connect with an unconfigured gateway must route the user to Settings")
	}
	if got.Tab != settings.TabBasic || got.Field != settings.FieldGateway {
		t.Errorf("routed to %s ▸ %s, want %s ▸ %s",
			got.Tab, got.Field, settings.TabBasic, settings.FieldGateway)
	}
	if got.Message == "" {
		t.Error("the issue must carry a banner message naming the fix")
	}

	// No tunnel may start, and no scary Error event may be emitted.
	select {
	case <-authCalled:
		t.Error("a doomed tunnel started despite the blocking config issue")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case e := <-a.events:
		t.Errorf("an event was emitted for a blocking issue (%v); the guidance lives in the banner now", e.State)
	default:
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
