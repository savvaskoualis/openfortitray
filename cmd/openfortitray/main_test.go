package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
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

// wantConnected is what onSystemWake consults (see its doc comment for why it is
// a separate atomic rather than reading lastNotified). Connect must set it and
// Disconnect must clear it, independent of whether the dial actually succeeds.
func TestWantConnectedTracksConnectAndDisconnect(t *testing.T) {
	a, authCalled := newTestApp(t, "vpn.example.com", t.TempDir())

	if a.wantConnected.Load() {
		t.Fatal("wantConnected must start false")
	}
	a.Connect()
	select {
	case <-authCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor never started authenticating")
	}
	if !a.wantConnected.Load() {
		t.Error("wantConnected must be true once Connect has started the supervisor")
	}
	a.Disconnect()
	if a.wantConnected.Load() {
		t.Error("wantConnected must be false once Disconnect is called")
	}
}

// A wake with nothing to resume (never connected, or already disconnected) must
// not dial — a wake notification arriving while the user is deliberately
// disconnected must never surprise them with a connection attempt.
func TestOnSystemWakeNoopWhenNotConnected(t *testing.T) {
	test.NewApp()
	a, authCalled := newTestApp(t, "vpn.example.com", t.TempDir())

	a.onSystemWake()

	select {
	case <-authCalled:
		t.Error("onSystemWake dialed despite wantConnected being false")
	case <-time.After(100 * time.Millisecond):
	}
}

// The real case: connected before sleep, so a wake must force a fresh
// Disconnect+Connect rather than trust a tunnel that may have died silently
// while the machine slept.
func TestOnSystemWakeForcesReconnectWhenWantConnected(t *testing.T) {
	test.NewApp()
	a, authCalled := newTestApp(t, "vpn.example.com", t.TempDir())

	a.Connect()
	select {
	case <-authCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor never started authenticating")
	}

	a.onSystemWake()

	select {
	case <-authCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("onSystemWake did not force a fresh reconnect")
	}
	if !a.wantConnected.Load() {
		t.Error("wantConnected must still be true after a wake-forced reconnect")
	}
}

// The update dialog must surface only ONCE per distinct version: the badge and
// menu item update on every 6-hourly check (cheap), but re-prompting the same
// version every 6h would nag. shouldPromptUpdate is the pure decision behind the
// thin promptUpdate wrapper (a headless fyne dialog is impractical to drive in a
// test); this pins its once-per-version contract.
func TestShouldPromptUpdateOncePerVersion(t *testing.T) {
	a := &app{}

	if !a.shouldPromptUpdate("v1.0.0") {
		t.Error("a newly-found version must prompt")
	}
	if a.shouldPromptUpdate("v1.0.0") {
		t.Error("the same version must not prompt again (no 6-hourly nag)")
	}
	if !a.shouldPromptUpdate("v1.1.0") {
		t.Error("a new distinct version must prompt again")
	}
	if a.shouldPromptUpdate("v1.1.0") {
		t.Error("the now-current version must not re-prompt")
	}
	if a.shouldPromptUpdate("") {
		t.Error("an empty tag must never prompt")
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

// shutdownWait must cover the backend's worst-case teardown so a clean quit never
// SIGKILLs openconnect before it has sent its FortiGate logout — a session left
// open is what rejects the next run's first cookie. The worst case is coupled to
// internal/tunnel's constants (helperStopAttempts*helperStopTimeout +
// helperWaitDelay = 2*10s + 12s = 32s); if those change, this and the doc comment
// must move with them.
func TestShutdownWaitCoversTeardownBudget(t *testing.T) {
	const teardownWorstCase = 32 * time.Second
	if shutdownWait < teardownWorstCase {
		t.Errorf("shutdownWait %v must be >= the teardown worst case %v, or a clean quit kills openconnect mid-logout",
			shutdownWait, teardownWorstCase)
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

// shouldResumeAfterUpdate must reflect whether the tunnel was actually
// connected, not merely "not disconnected" or "an update happened" — an
// update declined or applied with no active session must not fabricate a
// reconnect on the next launch.
func TestShouldResumeAfterUpdate(t *testing.T) {
	tests := []struct {
		state tunnel.State
		want  bool
	}{
		{tunnel.Connected, true},
		{tunnel.Disconnected, false},
		{tunnel.Connecting, false},
		{tunnel.Reconnecting, false},
		{tunnel.Error, false},
	}
	for _, tt := range tests {
		a := &app{lastNotified: tt.state}
		if got := a.shouldResumeAfterUpdate(); got != tt.want {
			t.Errorf("lastNotified=%v: shouldResumeAfterUpdate() = %v, want %v", tt.state, got, tt.want)
		}
	}
}

// The resume marker is a one-shot: finishUpdate writes it, the next launch
// must see it exactly once and then behave as if it were never there.
func TestResumeMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if consumeResumeMarker(dir) {
		t.Fatal("consumeResumeMarker on an empty dir = true, want false")
	}
	if err := writeResumeMarker(dir); err != nil {
		t.Fatalf("writeResumeMarker: %v", err)
	}
	if !consumeResumeMarker(dir) {
		t.Fatal("consumeResumeMarker after writeResumeMarker = false, want true")
	}
	if consumeResumeMarker(dir) {
		t.Fatal("consumeResumeMarker fired a second time; the marker must be one-shot")
	}
}

// newCookieTestApp builds an app wired to an in-memory credstore fake and a
// stubbed SAML flow that hands out FRESH-1, FRESH-2, … on each call, so the
// cache-first auth flow is exercised without a real keychain or a live gateway.
func newCookieTestApp(prof config.Profile) (*app, *credstore.Memory, *int) {
	mem := credstore.NewMemory()
	n := 0
	a := &app{
		cookieGet:    mem.Get,
		cookieSet:    mem.Set,
		cookieDelete: mem.Delete,
		samlAuth: func(ctx context.Context, p config.Profile) (string, error) {
			n++
			return fmt.Sprintf("FRESH-%d", n), nil
		},
	}
	a.setSnapshot(tunnelParams{prof: prof})
	a.storedCookieTried.Store(false) // startTunnel does this before every Connect
	return a, mem, &n
}

// A fresh Connect with a valid stored cookie must reconnect with NO browser: the
// first authenticate returns the stored cookie and never runs SAML.
func TestAuthenticateReusesStoredCookie(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: true}
	a, mem, saml := newCookieTestApp(prof)
	mem.Set(cookieKey("vpn.example.com"), "STORED-COOKIE")

	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "STORED-COOKIE" {
		t.Errorf("cookie = %q, want STORED-COOKIE", got)
	}
	if *saml != 0 {
		t.Errorf("SAML ran %d times; a valid stored cookie must open no browser", *saml)
	}
}

// The supervisor re-mints on a rejected stored cookie: the second authenticate of
// the same Connect (flag already set) must run SAML and store the fresh cookie.
func TestAuthenticateRemintsAfterRejection(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: true}
	a, mem, saml := newCookieTestApp(prof)
	mem.Set(cookieKey("vpn.example.com"), "DEAD-COOKIE")

	if got, _ := a.authenticate(context.Background()); got != "DEAD-COOKIE" {
		t.Fatalf("first auth = %q, want the stored DEAD-COOKIE", got)
	}
	// Gateway rejected it → supervisor calls authFn again, same Connect.
	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "FRESH-1" || *saml != 1 {
		t.Errorf("re-mint = %q (saml=%d), want a fresh SAML cookie", got, *saml)
	}
	if stored, _ := mem.Get(cookieKey("vpn.example.com")); stored != "FRESH-1" {
		t.Errorf("stored cookie = %q, want the fresh FRESH-1 persisted", stored)
	}
}

// No stored cookie: SAML runs and the minted cookie is persisted for next time.
func TestAuthenticateStoresFreshCookie(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: true}
	a, mem, saml := newCookieTestApp(prof)

	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "FRESH-1" || *saml != 1 {
		t.Errorf("auth = %q (saml=%d), want a fresh SAML cookie", got, *saml)
	}
	if stored, _ := mem.Get(cookieKey("vpn.example.com")); stored != "FRESH-1" {
		t.Errorf("stored cookie = %q, want FRESH-1", stored)
	}
}

// remember_session off: never reuse a stored cookie, always SAML, never store.
func TestAuthenticateSkipsStoreWhenRememberOff(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: false}
	a, mem, saml := newCookieTestApp(prof)
	mem.Set(cookieKey("vpn.example.com"), "STORED-COOKIE")

	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "FRESH-1" || *saml != 1 {
		t.Errorf("auth = %q (saml=%d), want SAML ignoring the stored cookie", got, *saml)
	}
	// The pre-existing stored cookie must be left untouched (never overwritten).
	if stored, _ := mem.Get(cookieKey("vpn.example.com")); stored != "STORED-COOKIE" {
		t.Errorf("stored cookie = %q; remember-off must not write", stored)
	}
}

// A keychain that is temporarily busy (credstore.ErrBusy — e.g. the macOS login
// keychain mid-unlock at login) must be retried, not treated as a miss: the
// stored cookie is still there, the store just cannot answer yet.
func TestAuthenticateRetriesOnBusyStoreThenReuses(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: true}
	a, mem, saml := newCookieTestApp(prof)
	a.cookieRetryInterval = time.Millisecond
	a.cookieRetryWindow = time.Second
	mem.Set(cookieKey("vpn.example.com"), "STORED-COOKIE")

	var calls int
	real := a.cookieGet
	a.cookieGet = func(key string) (string, error) {
		calls++
		if calls < 3 {
			return "", credstore.ErrBusy
		}
		return real(key)
	}

	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "STORED-COOKIE" {
		t.Errorf("cookie = %q, want STORED-COOKIE", got)
	}
	if calls != 3 {
		t.Errorf("cookieGet called %d times, want 3 (two busy + one success)", calls)
	}
	if *saml != 0 {
		t.Errorf("SAML ran %d times; a busy-then-valid store must open no browser", *saml)
	}
}

// A store that never stops reporting busy must eventually give up and fall
// back to SAML rather than hanging or looping forever.
func TestAuthenticateGivesUpOnPermanentlyBusyStore(t *testing.T) {
	prof := config.Profile{Name: "P", Gateway: "vpn.example.com", RememberSession: true}
	a, _, saml := newCookieTestApp(prof)
	a.cookieRetryInterval = time.Millisecond
	a.cookieRetryWindow = 20 * time.Millisecond
	a.cookieGet = func(key string) (string, error) { return "", credstore.ErrBusy }

	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "FRESH-1" || *saml != 1 {
		t.Errorf("auth = %q (saml=%d), want a fallback SAML run once the retry window elapses", got, *saml)
	}
}

// reconcileStoredCookies deletes the stored cookie exactly when an edit makes it
// stale or unwanted: remember turned off, gateway changed, or auth method changed
// — and leaves it in place otherwise.
func TestReconcileStoredCookies(t *testing.T) {
	base := config.Profile{Name: "P", Gateway: "g1.example.com", RememberSession: true,
		Auth: config.AuthConfig{Method: config.AuthSAML}}
	cfg := func(p config.Profile) *config.Config {
		return &config.Config{ActiveProfile: "P", Profiles: []config.Profile{p}}
	}
	mut := func(f func(*config.Profile)) config.Profile {
		p := base
		f(&p)
		return p
	}

	tests := []struct {
		name       string
		new        config.Profile
		wantDelete bool
	}{
		{"unchanged keeps cookie", base, false},
		{"remember turned off deletes", mut(func(p *config.Profile) { p.RememberSession = false }), true},
		{"gateway change deletes", mut(func(p *config.Profile) { p.Gateway = "g2.example.com" }), true},
		{"auth method change deletes", mut(func(p *config.Profile) { p.Auth.Method = config.AuthCert }), true},
		{"unrelated edit keeps cookie", mut(func(p *config.Profile) { p.DTLS = !p.DTLS }), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mem := credstore.NewMemory()
			mem.Set(cookieKey("g1.example.com"), "COOKIE")
			a := &app{cookieDelete: mem.Delete}

			a.reconcileStoredCookies(cfg(base), cfg(tc.new))

			stored, _ := mem.Get(cookieKey("g1.example.com"))
			deleted := stored == ""
			if deleted != tc.wantDelete {
				t.Errorf("deleted = %v, want %v (stored=%q)", deleted, tc.wantDelete, stored)
			}
		})
	}
}

// The Windows build bundles openconnect at <exeDir>\openconnect\openconnect.exe.
// resolveBundledOpenconnect (the OS-independent core of resolveOpenconnectPath)
// must swap the bare "openconnect" default for that bundled binary only when it
// exists, never override an explicit user path, and fall back to the configured
// value when the bundle is absent. The exists func is injected so the three cases
// are exercised on any host, not just Windows.
func TestResolveBundledOpenconnect(t *testing.T) {
	exeDir := filepath.FromSlash("/opt/openfortitray")
	bundled := filepath.Join(exeDir, "openconnect", "openconnect.exe")
	present := func(p string) bool { return p == bundled }
	absent := func(string) bool { return false }
	userPath := filepath.FromSlash("C:/tools/openconnect.exe")

	tests := []struct {
		name       string
		configured string
		exists     func(string) bool
		want       string
	}{
		{"bare default + bundle present → bundled path", defaultOpenconnectName, present, bundled},
		{"explicit user path → unchanged", userPath, present, userPath},
		{"bare default + bundle absent → falls back to configured", defaultOpenconnectName, absent, defaultOpenconnectName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBundledOpenconnect(tc.configured, exeDir, tc.exists); got != tc.want {
				t.Errorf("resolveBundledOpenconnect(%q, %q) = %q, want %q", tc.configured, exeDir, got, tc.want)
			}
		})
	}
}

// slowSupervisor makes the teardown take measurable time, so a test can tell
// whether the caller actually waited for it.
type slowSupervisor struct {
	delay time.Duration
	mu    sync.Mutex
	done  bool
}

func (s *slowSupervisor) Connect()    {}
func (s *slowSupervisor) Disconnect() {}
func (s *slowSupervisor) Wait(ctx context.Context) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}
func (s *slowSupervisor) finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// awaitShutdown must not return until the tunnel teardown has finished.
//
// This is the regression test for a real leak: fyne installs its OWN
// SIGINT/SIGTERM handler (gLDriver.catchTerm) that calls Quit immediately, and Go
// delivers a signal to every registered channel. So a SIGTERM ended the fyne run
// loop while our teardown was still running on its goroutine, main returned, and
// the process died before openconnect could send its FortiGate logout — leaving a
// server-side session that refused every new cookie for minutes.
func TestAwaitShutdownBlocksUntilTeardownFinishes(t *testing.T) {
	ss := &slowSupervisor{delay: 150 * time.Millisecond}
	a := &app{sup: ss}

	start := time.Now()
	a.awaitShutdown()
	if !ss.finished() {
		t.Error("awaitShutdown returned while the teardown was still running; the process would exit mid-logout")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("awaitShutdown returned after %v; it cannot have waited for the teardown", elapsed)
	}
}

// The common path: the tray's Quit already ran the teardown, so awaitShutdown
// just observes it and returns — it must neither hang nor re-run the teardown.
func TestAwaitShutdownAfterQuitDoesNotRerun(t *testing.T) {
	fs := &fakeSupervisor{}
	a := &app{sup: fs}

	quit := make(chan struct{}, 1)
	a.shutdown(func() { quit <- struct{}{} })
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("the initial shutdown never completed")
	}

	done := make(chan struct{})
	go func() { a.awaitShutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("awaitShutdown hung after a shutdown that had already finished")
	}
	if _, disconnects, _ := fs.counts(); disconnects != 1 {
		t.Errorf("Disconnect called %d times, want exactly 1 (awaitShutdown must not re-tear-down)", disconnects)
	}
}

// A stored cookie the gateway rejected must be dropped, not kept for the next
// Connect to fail on again. Measured against a real gateway, a reused SVPNCOOKIE
// was refused on all 30 attempts — it is bound to its server-side session, so it
// cannot start working later.
func TestRejectedStoredCookieIsDropped(t *testing.T) {
	mem := credstore.NewMemory()
	if err := mem.Set(cookieKey("gw.example.com"), "DEAD"); err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg: &config.Config{
			ActiveProfile: "Default",
			Profiles: []config.Profile{{
				Name: "Default", Gateway: "gw.example.com", Port: 10443,
				RememberSession: true,
			}},
		},
		cookieGet:    mem.Get,
		cookieSet:    mem.Set,
		cookieDelete: mem.Delete,
		samlAuth: func(ctx context.Context, p config.Profile) (string, error) {
			return "FRESH", nil
		},
	}
	a.setSnapshot(tunnelParams{prof: *a.cfg.Active()})

	// First auth of this Connect: the stored cookie is offered, no browser.
	got, err := a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "DEAD" {
		t.Fatalf("first auth = %q, want the stored cookie DEAD", got)
	}

	// The gateway rejected it, so the supervisor asks again: SAML runs, and the
	// dead cookie must be gone — replaced by the fresh one, not left to be retried.
	got, err = a.authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "FRESH" {
		t.Fatalf("second auth = %q, want a fresh SAML cookie", got)
	}
	if v, _ := mem.Get(cookieKey("gw.example.com")); v != "FRESH" {
		t.Errorf("stored cookie = %q, want the rejected one replaced by FRESH", v)
	}
}

// The status window's detail card reads these two, so they must be right for the
// shapes a saved config actually takes — including the empty gateway a first run
// has, where a naive JoinHostPort would render a stray ":10443".
func TestGatewayLabelAndDTLSLabel(t *testing.T) {
	cases := []struct {
		name        string
		prof        config.Profile
		wantGateway string
		wantDTLS    string
	}{
		{
			name:        "default port",
			prof:        config.Profile{Name: "p", Gateway: "vpn.example.com", Port: 10443, DTLS: true},
			wantGateway: "vpn.example.com:10443",
			wantDTLS:    "DTLS on",
		},
		{
			name:        "custom port, DTLS off",
			prof:        config.Profile{Name: "p", Gateway: "vpn.example.com", Port: 8443},
			wantGateway: "vpn.example.com:8443",
			wantDTLS:    "DTLS off",
		},
		{
			// A first run has no gateway. An empty label makes the card render a
			// dash; building "…:10443" from nothing would look like a real value.
			name:        "unconfigured gateway yields an empty label",
			prof:        config.Profile{Name: "p", Port: 10443},
			wantGateway: "",
			wantDTLS:    "DTLS off",
		},
		{
			// An IPv6 literal has to come back bracketed, or the string is
			// ambiguous and unusable.
			name:        "IPv6 literal is bracketed",
			prof:        config.Profile{Name: "p", Gateway: "2001:db8::1", Port: 10443},
			wantGateway: "[2001:db8::1]:10443",
			wantDTLS:    "DTLS off",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{cfg: &config.Config{ActiveProfile: "p", Profiles: []config.Profile{tc.prof}}}
			if got := a.GatewayLabel(); got != tc.wantGateway {
				t.Errorf("GatewayLabel() = %q, want %q", got, tc.wantGateway)
			}
			if got := a.DTLSLabel(); got != tc.wantDTLS {
				t.Errorf("DTLSLabel() = %q, want %q", got, tc.wantDTLS)
			}
		})
	}
}
