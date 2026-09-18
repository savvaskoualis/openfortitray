package main

import (
	"embed"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

//go:embed testdata_wailsapp/dummy.txt
var testAssets embed.FS

func TestBuildAppOptionsFrameless380x600(t *testing.T) {
	a := &app{}
	opts := buildAppOptions(a, testAssets)

	if !opts.Frameless {
		t.Error("expected Frameless: true")
	}
	if opts.Width != 380 || opts.Height != 600 {
		t.Errorf("expected 380x600, got %dx%d", opts.Width, opts.Height)
	}
	if opts.Title != "OpenFortiTray" {
		t.Errorf("expected title OpenFortiTray, got %q", opts.Title)
	}
	if !opts.StartHidden {
		t.Error("expected StartHidden: true — a tray app must not pop its window up unasked at every launch")
	}
	if len(opts.Bind) != 1 {
		t.Fatalf("expected exactly one bound object, got %d", len(opts.Bind))
	}
	if _, ok := opts.Bind[0].(*Bridge); !ok {
		t.Errorf("expected bound object to be *Bridge, got %T", opts.Bind[0])
	}
}

// TestBridgeRecentActivityRoundTrip proves the Add-then-read round trip:
// an event recorded via a.activity.Add (as pump() does, mu-guarded) is
// visible through Bridge.RecentActivity (which calls a.recentActivity(),
// the same mu-guarded accessor).
func TestBridgeRecentActivityRoundTrip(t *testing.T) {
	a := &app{activity: uistate.NewRing(50)}
	a.mu.Lock()
	a.activity.Add(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"}, time.Now())
	a.mu.Unlock()

	b := &Bridge{a: a}
	got := b.RecentActivity()
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(got), got)
	}
	if want := "Connected — 10.0.0.88"; got[0].Text != want {
		t.Errorf("expected Text %q, got %q", want, got[0].Text)
	}
}

// TestBridgeRecentActivityNilRing proves RecentActivity is nil-safe before
// activity is constructed (e.g. a bare &app{} in other tests), mirroring the
// nil check in recentActivity().
func TestBridgeRecentActivityNilRing(t *testing.T) {
	b := &Bridge{a: &app{}}
	if got := b.RecentActivity(); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestBridgeSaveConfigValidationFailures proves SaveConfig routes an invalid
// config to its validation-failure path (settings.Validate) rather than ever
// reaching Commit — the one place untrusted-shaped JS data reaches Go, so a
// malformed value here must never fall through to being persisted. Each case
// uses a bare &app{} (a.cfg stays nil): validation happens on the *passed-in*
// cfg, before settingsHost().Commit is ever called, so an invalid cfg never
// needs a working Commit path.
func TestBridgeSaveConfigValidationFailures(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.Config
		wantSubstr string
	}{
		{
			name:       "no profiles at all",
			cfg:        config.Config{},
			wantSubstr: "at least one profile",
		},
		{
			name: "gateway carries a scheme/port instead of a bare host",
			cfg: config.Config{Profiles: []config.Profile{
				{Name: "Default", Gateway: "https://vpn.example.com:10443"},
			}},
			wantSubstr: "host only",
		},
		{
			name: "custom port out of range",
			cfg: config.Config{Profiles: []config.Profile{
				{Name: "Default", Gateway: "vpn.example.com", CustomPort: true, Port: 70000},
			}},
			wantSubstr: "port must be between",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bridge{a: &app{}}
			issue := b.SaveConfig(tt.cfg)
			if issue == nil {
				t.Fatal("expected a validation issue, got nil")
			}
			if !strings.Contains(issue.Message, tt.wantSubstr) {
				t.Errorf("issue.Message = %q, want a substring %q", issue.Message, tt.wantSubstr)
			}
		})
	}
}

// TestBridgeGatewayLabelDTLSLabelVersionDelegate proves the Finding 7/10
// Bridge accessors are plain delegations to the existing app methods
// (GatewayLabel/DTLSLabel already have their own thorough table tests in
// main_test.go; this only proves Bridge forwards to them, and to Version,
// without transforming the result).
func TestBridgeGatewayLabelDTLSLabelVersionDelegate(t *testing.T) {
	a := &app{cfg: &config.Config{
		ActiveProfile: "p",
		Profiles:      []config.Profile{{Name: "p", Gateway: "vpn.example.com", Port: 10443, DTLS: true}},
	}}
	b := &Bridge{a: a}

	if got, want := b.GatewayLabel(), a.GatewayLabel(); got != want {
		t.Errorf("Bridge.GatewayLabel() = %q, want %q (a.GatewayLabel())", got, want)
	}
	if got, want := b.DTLSLabel(), a.DTLSLabel(); got != want {
		t.Errorf("Bridge.DTLSLabel() = %q, want %q (a.DTLSLabel())", got, want)
	}
	if got, want := b.Version(), a.Version(); got != want {
		t.Errorf("Bridge.Version() = %q, want %q (a.Version())", got, want)
	}
}

// TestRecentActivityConcurrentAddAndRead is the concurrency-safety proof this
// task introduces: uistate.Ring is explicitly not safe for concurrent use on
// its own, so a.mu must genuinely serialize pump()'s Add against
// recentActivity()'s Entries() read. Run with -race to confirm.
func TestRecentActivityConcurrentAddAndRead(t *testing.T) {
	a := &app{activity: uistate.NewRing(50)}

	var wg sync.WaitGroup
	wg.Add(2)

	// Simulates pump(): one goroutine appending events.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			e := tunnel.Event{State: tunnel.Connecting, Detail: time.Now().String()}
			a.mu.Lock()
			a.activity.Add(e, time.Now())
			a.mu.Unlock()
		}
	}()

	// Simulates Wails' JS bridge goroutine: concurrently reading via the
	// exact path Bridge.RecentActivity uses.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = a.recentActivity()
		}
	}()

	wg.Wait()

	got := a.recentActivity()
	if len(got) == 0 {
		t.Fatal("expected at least one entry after concurrent adds")
	}
	if len(got) > 50 {
		t.Errorf("expected at most 50 entries (ring capacity), got %d", len(got))
	}
}
