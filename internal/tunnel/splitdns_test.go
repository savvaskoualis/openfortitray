package tunnel

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// dnsRecorder records the dns-set/dns-clear commands the wiring shells out to.
type dnsRecorder struct {
	mu    sync.Mutex
	calls [][]string // each entry is name followed by args
}

func (r *dnsRecorder) run(ctx context.Context, name string, args []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

func (r *dnsRecorder) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// find returns the first recorded call whose subcommand is sub, or nil. The argv
// is [sudo, -n, <helper>, <subcommand>, ...], so the subcommand is at index 3.
func (r *dnsRecorder) find(sub string) []string {
	for _, c := range r.snapshot() {
		if len(c) >= 4 && c[3] == sub {
			return c
		}
	}
	return nil
}

func waitForCall(t *testing.T, r *dnsRecorder, sub string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if c := r.find(sub); c != nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dns %q was never invoked; recorded %v", sub, r.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The heart of the feature: once the tunnel reports Connected with a non-empty
// SplitDNS, the discovered DNS is installed via `dns-set <ip> <domains...>`, and
// on teardown the domains are removed via `dns-clear <domains...>`.
func TestSplitDNSSetOnConnectClearOnTeardown(t *testing.T) {
	rec := &dnsRecorder{}
	opts := Options{
		HelperPath:  "/opt/h",
		UseSudo:     true,
		SplitDNS:    []string{"corp.private", "svc.corp.private"},
		discoverDNS: func(ctx context.Context, hint []string) (string, error) { return "10.10.0.4", nil },
		dnsRunner:   rec.run,
	}
	inner := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	}
	run := opts.withSplitDNS(inner)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, "COOKIE", func(string) {}) }()

	set := waitForCall(t, rec, "dns-set", 3*time.Second)
	wantSet := []string{"sudo", "-n", "/opt/h", "dns-set", "10.10.0.4", "corp.private", "svc.corp.private"}
	if !slices.Equal(set, wantSet) {
		t.Errorf("dns-set argv = %v, want %v", set, wantSet)
	}

	cancel() // Disconnect: teardown must clear our scoped resolvers
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancel")
	}

	clear := rec.find("dns-clear")
	wantClear := []string{"sudo", "-n", "/opt/h", "dns-clear", "corp.private", "svc.corp.private"}
	if !slices.Equal(clear, wantClear) {
		t.Errorf("dns-clear argv = %v, want %v", clear, wantClear)
	}
}

// The passed-in discovered DNS must actually reach dns-set — a stub returning a
// distinctive address proves the value is threaded through rather than hardcoded.
func TestSplitDNSUsesDiscoveredServer(t *testing.T) {
	rec := &dnsRecorder{}
	var gotHint []string
	opts := Options{
		HelperPath: "/opt/h",
		UseSudo:    true,
		SplitDNS:   []string{"a.corp"},
		discoverDNS: func(ctx context.Context, hint []string) (string, error) {
			gotHint = hint
			return "172.16.9.9", nil
		},
		dnsRunner: rec.run,
	}
	run := opts.withSplitDNS(func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = run(ctx, "C", func(string) {}) }()

	set := waitForCall(t, rec, "dns-set", 3*time.Second)
	if set[4] != "172.16.9.9" {
		t.Errorf("dns-set used %q as the DNS server, want the discovered 172.16.9.9", set[4])
	}
	if !slices.Equal(gotHint, []string{"a.corp"}) {
		t.Errorf("discovery got hint %v, want the split-DNS domains [a.corp]", gotHint)
	}
}

// Discovery failure must not take down the tunnel or invoke dns-set — split-DNS
// is best-effort. dns-clear still runs on teardown (idempotent).
func TestSplitDNSDiscoveryFailureIsBestEffort(t *testing.T) {
	// Shrink the retry wait so the failing-discovery loop does not stall the test.
	defer func(a int, i time.Duration) { dnsDiscoverAttempts, dnsDiscoverInterval = a, i }(dnsDiscoverAttempts, dnsDiscoverInterval)
	dnsDiscoverAttempts, dnsDiscoverInterval = 3, time.Millisecond

	rec := &dnsRecorder{}
	opts := Options{
		HelperPath:  "/opt/h",
		UseSudo:     true,
		SplitDNS:    []string{"corp.private"},
		discoverDNS: func(ctx context.Context, hint []string) (string, error) { return "", ErrNoDNSForTest },
		dnsRunner:   rec.run,
	}
	inner := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	}
	run := opts.withSplitDNS(inner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, "C", func(string) {}) }()
	// Give the discovery retry loop time to exhaust and NOT call dns-set.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if c := rec.find("dns-set"); c != nil {
		t.Errorf("dns-set was invoked despite discovery failure: %v", c)
	}
	if c := rec.find("dns-clear"); c == nil {
		t.Error("dns-clear must still run on teardown, even when nothing was set")
	}
}

// With no domains, or on the direct (non-sudo) path, split-DNS is inert: the
// wrapper returns the runFn unchanged and never shells out.
func TestSplitDNSDisabledCases(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"no domains", Options{HelperPath: "/opt/h", UseSudo: true}},
		{"direct path (no privileged helper)", Options{OpenconnectPath: "openconnect", SplitDNS: []string{"corp.private"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &dnsRecorder{}
			tc.opts.discoverDNS = func(ctx context.Context, hint []string) (string, error) { return "10.0.0.1", nil }
			tc.opts.dnsRunner = rec.run
			called := false
			inner := func(ctx context.Context, cookie string, connected func(string)) error {
				called = true
				connected("10.0.0.5")
				return nil
			}
			run := tc.opts.withSplitDNS(inner)
			if err := run(context.Background(), "C", func(string) {}); err != nil {
				t.Fatalf("run returned %v", err)
			}
			if !called {
				t.Error("the wrapped runFn must still be invoked")
			}
			if calls := rec.snapshot(); len(calls) != 0 {
				t.Errorf("split-DNS disabled but shelled out: %v", calls)
			}
		})
	}
}

func TestDNSArgv(t *testing.T) {
	o := Options{HelperPath: "/opt/custom/h", UseSudo: true, SplitDNS: []string{"a.b", "c.d"}}
	name, args := o.dnsSetArgv("10.10.0.4")
	if name != "sudo" || !slices.Equal(args, []string{"-n", "/opt/custom/h", "dns-set", "10.10.0.4", "a.b", "c.d"}) {
		t.Errorf("dnsSetArgv = %q %q", name, args)
	}
	name, args = o.dnsClearArgv()
	if name != "sudo" || !slices.Equal(args, []string{"-n", "/opt/custom/h", "dns-clear", "a.b", "c.d"}) {
		t.Errorf("dnsClearArgv = %q %q", name, args)
	}
	// Empty helper path falls back to the installed location, mirroring stopArgv.
	if _, args := (Options{UseSudo: true, SplitDNS: []string{"x"}}).dnsSetArgv("1.2.3.4"); args[1] != DefaultHelperPath {
		t.Errorf("empty helper path did not fall back to %s: %v", DefaultHelperPath, args)
	}
}

// ErrNoDNSForTest stands in for a discovery failure without importing the dns
// package's sentinel (any non-nil error suppresses dns-set).
var ErrNoDNSForTest = errTest("no dns")

type errTest string

func (e errTest) Error() string { return string(e) }
