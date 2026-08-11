package main

import (
	"context"
	"os"
	"slices"
	"sync"
	"syscall"
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

// fakeSupervisor records the teardown calls the graceful-shutdown path makes, so
// tests can assert Disconnect+Wait ran once and were not double-run.
type fakeSupervisor struct {
	mu          sync.Mutex
	connects    int
	disconnects int
	waits       int
}

func (f *fakeSupervisor) Connect()                 { f.mu.Lock(); f.connects++; f.mu.Unlock() }
func (f *fakeSupervisor) Disconnect()              { f.mu.Lock(); f.disconnects++; f.mu.Unlock() }
func (f *fakeSupervisor) Wait(ctx context.Context) { f.mu.Lock(); f.waits++; f.mu.Unlock() }

func (f *fakeSupervisor) counts() (connects, disconnects, waits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects, f.disconnects, f.waits
}

// A signal must tear the tunnel down through the same graceful path the tray Quit
// uses — otherwise an abrupt exit orphans a root openconnect. And a second signal
// must not re-run the teardown (shutdownOnce), so two SIGTERMs still disconnect
// exactly once.
func TestSignalTriggersGracefulShutdownOnce(t *testing.T) {
	fs := &fakeSupervisor{}
	a := &app{sup: fs}

	quit := make(chan struct{}, 2)
	sigs := make(chan os.Signal, 2)
	sigs <- syscall.SIGTERM
	sigs <- syscall.SIGTERM // the second must be a no-op
	close(sigs)

	handlerDone := make(chan struct{})
	go func() {
		a.watchSignals(sigs, func() { quit <- struct{}{} })
		close(handlerDone)
	}()

	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("a signal did not trigger the graceful shutdown")
	}
	<-handlerDone
	time.Sleep(50 * time.Millisecond) // give an erroneous second run a chance to land

	_, disconnects, waits := fs.counts()
	if disconnects != 1 {
		t.Errorf("Disconnect called %d times, want exactly 1 (a second signal must not double-run)", disconnects)
	}
	if waits != 1 {
		t.Errorf("Wait called %d times, want exactly 1", waits)
	}
	if len(quit) != 0 {
		t.Errorf("quit fired %d extra times: the teardown double-ran", len(quit))
	}
	if !a.quitting.Load() {
		t.Error("shutdown must set the quitting guard so the event pump stops touching the UI")
	}
}

// Startup must reap a stale/orphaned tunnel BEFORE connecting on launch: minting
// a new cookie while a previous crash's root openconnect still holds the
// one-per-user FortiGate session is exactly what triggers the "Cookie was
// rejected" loop.
func TestSelfHealReapsBeforeConnect(t *testing.T) {
	var mu sync.Mutex
	var order []string
	reap := func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "reap")
		mu.Unlock()
		return nil
	}
	connect := func() {
		mu.Lock()
		order = append(order, "connect")
		mu.Unlock()
	}

	(&app{}).selfHealThenConnect(reap, true, connect)

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"reap", "connect"}) {
		t.Errorf("startup order = %v, want [reap connect]", order)
	}
}

// With autostart off we still self-heal (clearing a stale gateway session even if
// the user does not immediately reconnect) but must not connect on launch.
func TestSelfHealWithoutAutostartReapsButDoesNotConnect(t *testing.T) {
	var order []string
	reap := func(ctx context.Context) error { order = append(order, "reap"); return nil }
	connect := func() { order = append(order, "connect") }

	(&app{}).selfHealThenConnect(reap, false, connect)

	if !slices.Equal(order, []string{"reap"}) {
		t.Errorf("order = %v, want [reap] only: reap always runs, connect must not", order)
	}
}
