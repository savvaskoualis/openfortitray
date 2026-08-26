# IPsec Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a profile with `Backend: BackendIPsec` actually connects, on
macOS, Windows and Linux, using discrete Settings fields (PSK or
certificate auth, custom IKE/ESP proposals) that cover any valid IKEv2
configuration — replacing the current "IPsec is not yet supported" refusal
with a real connection.

**Architecture:** a new `internal/ipsec` package owns the IPsec connection
end to end, independent of `internal/tunnel` (openconnect stays untouched).
`ipsec.Supervisor` implements the same shape `cmd/openfortitray`'s
`supervisor` interface already expects (`Connect`/`Disconnect`/`Wait`/
`SetKeepAlive`) and writes `tunnel.Event` values onto the app's existing
event channel — the Status window, tray, and notifications need zero
changes. Per-platform connection mechanics live inside `internal/ipsec` via
build tags: `strongswan_unix.go` (darwin + linux) drives strongSwan's
`swanctl` CLI against its already-privileged `charon` daemon; `ipsec_windows.go`
drives the native Windows IKEv2 VPN API via PowerShell.

**Tech Stack:** Go, strongSwan (`swanctl`/`charon`, external dependency on
macOS/Linux, matching how `openconnect` is already an external dependency),
Windows' built-in VPN stack via PowerShell (`Add-VpnConnection`,
`rasdial`, `Get-VpnConnection`) — no new Go dependency.

**Spec:** `docs/superpowers/specs/2026-08-26-ipsec-runtime-design.md`

## Global Constraints

- No new privileged helper: strongSwan's `charon` runs as its own
  independently-started service; the app only ever talks to it unprivileged
  through `swanctl`. Windows' native VPN stack is privileged internally the
  same way.
- IKEv2 only — no version selector, no IKEv1 path, on any platform.
- The PSK secret is NEVER written to `config.json` — it lives in
  `credstore`, under the key `"openfortitray:ipsec-psk:" + gateway`
  (distinct from SSL's `"openfortitray:" + gateway`, since both backends
  can share a `Gateway` value on different profiles).
- Every native call (`swanctl`, log tailing, PowerShell VPN cmdlets) is
  best-effort: a failure is logged and surfaced through the existing
  connect-issue banner path, never a crash.
- `internal/ipsec` may import `internal/tunnel` only for the shared
  `tunnel.Event`/`tunnel.State` types; `internal/tunnel` must never import
  `internal/ipsec` (no cycle, and the SSL path stays untouched).
- Every step must leave `go build ./...` / `go vet ./...` / the full test
  suite passing on darwin (the dev machine). Windows behavior is
  cross-compile/syntax-verified only, never behaviorally tested here — say
  so honestly in reports, matching the standard the glass UI plan already
  established. macOS is the one platform where the strongSwan path can be
  behaviorally tested on this machine (Homebrew has a `strongswan` formula).
- Default IKE/ESP proposal: `"aes256-sha256-modp2048"`.

---

### Task 1: Config data model

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.IPsecAuthMethod` (`IPsecAuthPSK`, `IPsecAuthCert`),
  `config.IPsecConfig` struct, `Profile.IPsec IPsecConfig` field,
  `config.IPsecPSKCredstoreKey(gateway string) string`.
- Consumes: nothing new.

- [ ] **Step 1: Add `IPsecAuthMethod` and `IPsecConfig` to `internal/config/config.go`**

  Add right after the existing `Backend`/`BackendSSL`/`BackendIPsec`
  block:

  ```go
  // IPsecAuthMethod distinguishes how the IKE peer is authenticated. IKEv2
  // only — see the Backend doc comment.
  type IPsecAuthMethod string

  const (
  	// IPsecAuthPSK is pre-shared-key authentication. The default, and the
  	// simplest to get working against a fresh gateway config.
  	IPsecAuthPSK IPsecAuthMethod = "psk"
  	// IPsecAuthCert is client-certificate authentication.
  	IPsecAuthCert IPsecAuthMethod = "cert"
  )

  // IPsecConfig holds the fields an IPsec (IKEv2-only) profile needs beyond
  // the Gateway field it already shares with SSL profiles. The PSK secret
  // itself is NEVER stored here — it lives in credstore under
  // IPsecPSKCredstoreKey(gateway), exactly like the SSL password/cookie.
  type IPsecConfig struct {
  	AuthMethod IPsecAuthMethod `json:"auth_method"`
  	// LocalID/RemoteID are IKE identities. RemoteID defaults to the
  	// profile's Gateway host when empty (see normalizeIPsecConfig).
  	LocalID  string `json:"local_id,omitempty"`
  	RemoteID string `json:"remote_id,omitempty"`
  	// CertPath/KeyPath are used only when AuthMethod == IPsecAuthCert.
  	CertPath string `json:"cert_path,omitempty"`
  	KeyPath  string `json:"key_path,omitempty"`
  	// IKEProposal/ESPProposal are strongSwan-style cipher-suite strings
  	// (e.g. "aes256-sha256-modp2048"). Pre-filled with a strong default,
  	// editable — this is what makes "any valid IKEv2 config" possible
  	// without a raw config-file paste-in.
  	IKEProposal string `json:"ike_proposal,omitempty"`
  	ESPProposal string `json:"esp_proposal,omitempty"`
  }

  // defaultIPsecProposal is the strong, widely-supported modern cipher suite
  // new profiles and normalizeIPsecConfig pre-fill IKEProposal/ESPProposal
  // with.
  const defaultIPsecProposal = "aes256-sha256-modp2048"

  // IPsecPSKCredstoreKey is the credstore key an IPsec profile's PSK secret
  // is stored/read under. Distinct from the SSL cookie key
  // ("openfortitray:"+gateway) so a gateway shared by an SSL and an IPsec
  // profile never collides.
  func IPsecPSKCredstoreKey(gateway string) string {
  	return "openfortitray:ipsec-psk:" + gateway
  }
  ```

- [ ] **Step 2: Add `IPsec IPsecConfig` to `Profile` and wire defaults**

  In `Profile`, add the field next to `Backend`:

  ```go
  	Auth    AuthConfig  `json:"auth"`
  	Backend Backend     `json:"backend"`
  	IPsec   IPsecConfig `json:"ipsec,omitempty"`
  	Realm   string      `json:"realm,omitempty"`
  ```

  In `defaultProfile()`, add:

  ```go
  		Backend:         BackendSSL,
  		IPsec: IPsecConfig{
  			AuthMethod:  IPsecAuthPSK,
  			IKEProposal: defaultIPsecProposal,
  			ESPProposal: defaultIPsecProposal,
  		},
  ```

  In `normalizeProfile`, add after the `Backend` backfill:

  ```go
  	if p.Backend == "" {
  		p.Backend = BackendSSL
  	}
  	normalizeIPsecConfig(&p.IPsec, p.Gateway)
  ```

  Add the new helper below `normalizeProfile`:

  ```go
  // normalizeIPsecConfig fills IPsecConfig fields whose zero value is invalid
  // with their default. RemoteID defaults to the profile's own Gateway host,
  // so a hand-edited or pre-v-ipsec file that never set it still has a usable
  // IKE remote identity.
  func normalizeIPsecConfig(ic *IPsecConfig, gateway string) {
  	if ic.AuthMethod == "" {
  		ic.AuthMethod = IPsecAuthPSK
  	}
  	if ic.IKEProposal == "" {
  		ic.IKEProposal = defaultIPsecProposal
  	}
  	if ic.ESPProposal == "" {
  		ic.ESPProposal = defaultIPsecProposal
  	}
  	if ic.RemoteID == "" {
  		ic.RemoteID = gateway
  	}
  }
  ```

- [ ] **Step 3: Write the failing tests**

  In `internal/config/config_test.go`:

  ```go
  func TestNewProfileDefaultsToPSKWithDefaultProposals(t *testing.T) {
  	p := NewProfile("Test")
  	if p.IPsec.AuthMethod != IPsecAuthPSK {
  		t.Errorf("AuthMethod = %q, want %q", p.IPsec.AuthMethod, IPsecAuthPSK)
  	}
  	if p.IPsec.IKEProposal != defaultIPsecProposal {
  		t.Errorf("IKEProposal = %q, want %q", p.IPsec.IKEProposal, defaultIPsecProposal)
  	}
  	if p.IPsec.ESPProposal != defaultIPsecProposal {
  		t.Errorf("ESPProposal = %q, want %q", p.IPsec.ESPProposal, defaultIPsecProposal)
  	}
  }

  func TestNormalizeIPsecConfigBackfillsRemoteIDFromGateway(t *testing.T) {
  	p := Profile{Name: "Test", Gateway: "vpn.example.com"}
  	normalizeProfile(&p)
  	if p.IPsec.RemoteID != "vpn.example.com" {
  		t.Errorf("RemoteID = %q, want %q", p.IPsec.RemoteID, "vpn.example.com")
  	}
  }

  func TestNormalizeIPsecConfigLeavesExplicitRemoteIDAlone(t *testing.T) {
  	p := Profile{Name: "Test", Gateway: "vpn.example.com",
  		IPsec: IPsecConfig{RemoteID: "custom-remote-id"}}
  	normalizeProfile(&p)
  	if p.IPsec.RemoteID != "custom-remote-id" {
  		t.Errorf("RemoteID = %q, want unchanged %q", p.IPsec.RemoteID, "custom-remote-id")
  	}
  }

  func TestIPsecPSKCredstoreKeyDistinctFromSSLCookieKey(t *testing.T) {
  	gw := "vpn.example.com"
  	if IPsecPSKCredstoreKey(gw) == gw {
  		t.Fatal("sanity: gateway alone must not be a valid key")
  	}
  	sslKey := "openfortitray:" + gw
  	if IPsecPSKCredstoreKey(gw) == sslKey {
  		t.Errorf("IPsecPSKCredstoreKey(%q) collides with the SSL cookie key %q", gw, sslKey)
  	}
  }
  ```

- [ ] **Step 4: Run the tests to verify they fail**

  Run: `go test ./internal/config/... -run 'TestNewProfileDefaultsToPSK|TestNormalizeIPsecConfig|TestIPsecPSKCredstoreKey' -v`
  Expected: FAIL to compile (`IPsecConfig`/`IPsecAuthPSK`/etc. undefined)
  until Step 1/2 land.

- [ ] **Step 5: Implement Steps 1-2, run tests to verify they pass**

  Run: `go test ./internal/config/... -run 'TestNewProfileDefaultsToPSK|TestNormalizeIPsecConfig|TestIPsecPSKCredstoreKey' -v`
  Expected: PASS.

- [ ] **Step 6: Run the full existing config suite (regression check)**

  Run: `go test ./internal/config/... -v`
  Expected: all PASS — in particular, the existing `TestMigrate` table and
  round-trip tests must still pass with the new `IPsec` field present
  (zero-valued on any profile literal that doesn't set it explicitly is
  fine since `json:"ipsec,omitempty"` and normalization both handle that).

- [ ] **Step 7: Commit**

  ```bash
  git add internal/config/config.go internal/config/config_test.go
  git commit -m "feat(config): add IPsecConfig (PSK/cert, proposals) to Profile"
  ```

---

### Task 2: `internal/ipsec` core package

**Files:**
- Create: `internal/ipsec/ipsec.go`
- Test: `internal/ipsec/ipsec_test.go`

**Interfaces:**
- Consumes: `tunnel.Event`, `tunnel.State` (from Task-1-independent
  `internal/tunnel`, already implemented).
- Produces: `ipsec.RunFunc`, `ipsec.Supervisor`, `ipsec.New(runFn RunFunc,
  events chan<- tunnel.Event) *Supervisor`, `(*Supervisor)
  Connect/Disconnect/Wait/SetKeepAlive` — the exact shape
  `cmd/openfortitray`'s `supervisor` interface requires, so Task 6 can
  construct one with zero interface changes on the `app` side.

This task is the platform-agnostic supervision loop: connect, retry with
backoff on failure, support `SetKeepAlive`, translate into `tunnel.Event`.
It does NOT talk to strongSwan or Windows directly — it calls an injected
`RunFunc`, exactly like `internal/tunnel.Supervisor` is built around an
injected `RunFunc`/`AuthFunc` (see `internal/tunnel/tunnel.go`'s `New`).
Unlike `tunnel.Supervisor`, this loop has no cookie-rejection/re-auth
concept (a PSK or a client cert doesn't expire mid-session the way a SAML
cookie does) — its retry logic is a flat backoff on any connect failure,
simpler than `tunnel.Supervisor`'s SAML-shaped state machine.

- [ ] **Step 1: Write the failing tests**

  ```go
  package ipsec

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/savvaskoualis/openfortitray/internal/tunnel"
  )

  func TestConnectRunsRunFuncAndEmitsConnected(t *testing.T) {
  	events := make(chan tunnel.Event, 8)
  	started := make(chan struct{}, 1)
  	run := func(ctx context.Context, connected func(ip string)) error {
  		started <- struct{}{}
  		connected("10.0.0.5")
  		<-ctx.Done()
  		return ctx.Err()
  	}
  	s := New(run, events)
  	s.backoffBase = time.Millisecond
  	s.Connect()
  	defer s.Disconnect()

  	select {
  	case <-started:
  	case <-time.After(2 * time.Second):
  		t.Fatal("RunFunc never started")
  	}

  	select {
  	case ev := <-events:
  		if ev.State != tunnel.Connected || ev.Detail != "10.0.0.5" {
  			t.Errorf("got %+v, want Connected/10.0.0.5", ev)
  		}
  	case <-time.After(2 * time.Second):
  		t.Fatal("no Connected event")
  	}
  }

  func TestDisconnectStopsTheLoop(t *testing.T) {
  	events := make(chan tunnel.Event, 8)
  	torndown := make(chan struct{})
  	run := func(ctx context.Context, connected func(ip string)) error {
  		connected("10.0.0.5")
  		<-ctx.Done()
  		close(torndown)
  		return ctx.Err()
  	}
  	s := New(run, events)
  	s.Connect()
  	time.Sleep(50 * time.Millisecond) // let it reach Connected
  	s.Disconnect()

  	select {
  	case <-torndown:
  	case <-time.After(2 * time.Second):
  		t.Fatal("Disconnect never cancelled the running RunFunc")
  	}
  }

  func TestFailedConnectRetriesWithBackoffThenEmitsReconnecting(t *testing.T) {
  	events := make(chan tunnel.Event, 8)
  	var attempts int32
  	run := func(ctx context.Context, connected func(ip string)) error {
  		attempts++
  		return errors.New("swanctl: no response from charon")
  	}
  	s := New(run, events)
  	s.backoffBase = 10 * time.Millisecond
  	s.backoffMax = 20 * time.Millisecond
  	s.Connect()
  	defer s.Disconnect()

  	deadline := time.After(2 * time.Second)
  	for attempts < 2 {
  		select {
  		case <-deadline:
  			t.Fatalf("only %d attempt(s) after 2s, want at least 2", attempts)
  		case <-time.After(10 * time.Millisecond):
  		}
  	}

  	var sawReconnecting bool
  	for {
  		select {
  		case ev := <-events:
  			if ev.State == tunnel.Reconnecting {
  				sawReconnecting = true
  			}
  		default:
  			if !sawReconnecting {
  				t.Error("never emitted Reconnecting after a failed connect")
  			}
  			return
  		}
  	}
  }

  func TestSetKeepAliveFalseStopsRetryingAfterHealthySession(t *testing.T) {
  	events := make(chan tunnel.Event, 8)
  	var attempts int32
  	run := func(ctx context.Context, connected func(ip string)) error {
  		attempts++
  		if attempts == 1 {
  			connected("10.0.0.5")
  			<-ctx.Done()
  			return ctx.Err() // first session: healthy, then externally torn down
  		}
  		<-ctx.Done()
  		return ctx.Err()
  	}
  	s := New(run, events)
  	s.backoffBase = 10 * time.Millisecond
  	s.minHealthy = 0
  	s.SetKeepAlive(false)
  	s.Connect()
  	defer s.Disconnect()
  	time.Sleep(100 * time.Millisecond)
  	s.Disconnect() // simulate the healthy session ending
  	time.Sleep(100 * time.Millisecond)

  	if attempts != 1 {
  		t.Errorf("attempts = %d, want 1 (no retry after a healthy session with KeepAlive off)", attempts)
  	}
  }
  ```

- [ ] **Step 2: Run the tests to verify they fail**

  Run: `go test ./internal/ipsec/... -v`
  Expected: FAIL to compile (package doesn't exist yet).

- [ ] **Step 3: Implement `internal/ipsec/ipsec.go`**

  ```go
  // Package ipsec supervises an IKEv2 IPsec connection. It is independent of
  // internal/tunnel (which stays openconnect-specific): the platform-specific
  // work of actually bringing an IPsec tunnel up and down lives behind the
  // injected RunFunc, implemented per-OS in strongswan_unix.go (darwin,
  // linux) and ipsec_windows.go (windows) — this file is the supervision loop
  // only, and is fully testable with a fake RunFunc.
  //
  // Unlike internal/tunnel.Supervisor, this loop has no cookie-rejection /
  // re-authentication concept: a PSK or client certificate doesn't expire
  // mid-session the way a SAML cookie does, so retry is a flat backoff on any
  // connect failure rather than tunnel.Supervisor's SAML-shaped state
  // machine.
  package ipsec

  import (
  	"context"
  	"log"
  	"sync"
  	"time"

  	"github.com/savvaskoualis/openfortitray/internal/tunnel"
  )

  // RunFunc runs the IPsec backend until the tunnel goes down or ctx is
  // cancelled. It calls connected(ip) once the backend reports the tunnel is
  // up. Implemented per-platform (strongswan_unix.go, ipsec_windows.go).
  type RunFunc func(ctx context.Context, connected func(ip string)) error

  // Supervisor keeps an IPsec tunnel up: runs the backend and reconnects with
  // exponential backoff until told to stop.
  type Supervisor struct {
  	runFn  RunFunc
  	events chan<- tunnel.Event

  	backoffBase time.Duration // exposed for tests
  	backoffMax  time.Duration
  	minHealthy  time.Duration // time connected before a drop counts as "was healthy"

  	mu     sync.Mutex
  	cancel context.CancelFunc
  	gen    uint64
  	done   chan struct{}

  	keepAlive bool
  }

  // New builds a Supervisor around runFn, writing every state transition onto
  // events.
  func New(runFn RunFunc, events chan<- tunnel.Event) *Supervisor {
  	return &Supervisor{
  		runFn:       runFn,
  		events:      events,
  		backoffBase: 15 * time.Second,
  		backoffMax:  2 * time.Minute,
  		minHealthy:  30 * time.Second,
  		keepAlive:   true,
  	}
  }

  // SetKeepAlive controls whether a drop AFTER the tunnel has been up at
  // least once this Connect is retried at all. Same contract as
  // tunnel.Supervisor.SetKeepAlive.
  func (s *Supervisor) SetKeepAlive(on bool) {
  	s.mu.Lock()
  	s.keepAlive = on
  	s.mu.Unlock()
  }

  func (s *Supervisor) keepAliveEnabled() bool {
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	return s.keepAlive
  }

  func (s *Supervisor) emit(gen uint64, st tunnel.State, detail string) {
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	if s.gen != gen {
  		return
  	}
  	select {
  	case s.events <- tunnel.Event{State: st, Detail: detail}:
  	default: // never block the loop on a slow UI
  	}
  }

  // Connect starts the supervision loop. Idempotent while running.
  func (s *Supervisor) Connect() {
  	s.mu.Lock()
  	if s.cancel != nil {
  		s.mu.Unlock()
  		return
  	}
  	ctx, cancel := context.WithCancel(context.Background())
  	s.cancel = cancel
  	s.gen++
  	gen := s.gen
  	done := make(chan struct{})
  	s.done = done
  	s.mu.Unlock()
  	go s.loop(ctx, gen, done)
  }

  // Disconnect stops the loop and the backend. Idempotent.
  func (s *Supervisor) Disconnect() {
  	s.mu.Lock()
  	cancel := s.cancel
  	s.cancel = nil
  	s.mu.Unlock()
  	if cancel != nil {
  		cancel()
  	}
  }

  // Wait blocks until the supervision loop has fully torn down, or ctx is
  // done.
  func (s *Supervisor) Wait(ctx context.Context) {
  	s.mu.Lock()
  	done := s.done
  	s.mu.Unlock()
  	if done == nil {
  		return
  	}
  	select {
  	case <-done:
  	case <-ctx.Done():
  	}
  }

  func (s *Supervisor) loop(ctx context.Context, gen uint64, done chan struct{}) {
  	defer close(done)
  	backoff := s.backoffBase
  	everConnected := false

  	for {
  		s.emit(gen, tunnel.Connecting, "")
  		connectedAt := time.Time{}
  		err := s.runFn(ctx, func(ip string) {
  			connectedAt = time.Now()
  			everConnected = true
  			backoff = s.backoffBase
  			s.emit(gen, tunnel.Connected, ip)
  		})

  		if ctx.Err() != nil {
  			s.emit(gen, tunnel.Disconnected, "")
  			return
  		}

  		wasHealthy := !connectedAt.IsZero() && time.Since(connectedAt) >= s.minHealthy
  		if everConnected && wasHealthy && !s.keepAliveEnabled() {
  			s.emit(gen, tunnel.Disconnected, "")
  			return
  		}

  		detail := ""
  		if err != nil {
  			detail = err.Error()
  			log.Printf("ipsec: connection attempt failed: %v", err)
  		}
  		s.emit(gen, tunnel.Reconnecting, detail)

  		select {
  		case <-ctx.Done():
  			s.emit(gen, tunnel.Disconnected, "")
  			return
  		case <-time.After(backoff):
  		}
  		backoff *= 2
  		if backoff > s.backoffMax {
  			backoff = s.backoffMax
  		}
  	}
  }
  ```

- [ ] **Step 4: Run the tests to verify they pass**

  Run: `go test ./internal/ipsec/... -v`
  Expected: all PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/ipsec/ipsec.go internal/ipsec/ipsec_test.go
  git commit -m "feat(ipsec): platform-agnostic IPsec supervision loop"
  ```

---

### Task 3: strongSwan runtime (macOS + Linux)

**Files:**
- Create: `internal/ipsec/strongswan_unix.go`
- Test: `internal/ipsec/strongswan_unix_test.go`

**Interfaces:**
- Consumes: `config.IPsecConfig`, `config.Profile` (gateway/proposals),
  `credstore.Get`.
- Produces: `NewStrongSwanRunFunc(profile config.Profile, psk string) RunFunc`
  — the `RunFunc` Task 6 passes to `ipsec.New` when `runtime.GOOS` is
  darwin or linux.

- [ ] **Step 1: Write the failing tests for the pure log-parsing piece**

  The connection-establishment log line format strongSwan's `charon` emits
  on a successful `swanctl --initiate` (verified against strongSwan 5.9.x's
  `charon-systemd`/`ipsec.log` output format):

  ```
  CHILD_SA oft-tun{1} established with SPIs c1234567_i c89abcde_o and TS 0.0.0.0/0 === 10.212.140.0/24
  ```

  and on failure:

  ```
  establishing CHILD_SA 'oft-tun' failed
  ```

  In `internal/ipsec/strongswan_unix_test.go`:

  ```go
  package ipsec

  import "testing"

  func TestParseCharonLineExtractsAssignedIP(t *testing.T) {
  	line := `CHILD_SA oft-tun{1} established with SPIs c1234567_i c89abcde_o and TS 0.0.0.0/0 === 10.212.140.5/32`
  	ip, established, failed := parseCharonLine(line)
  	if !established || failed {
  		t.Fatalf("established=%v failed=%v, want established=true failed=false", established, failed)
  	}
  	if ip != "10.212.140.5" {
  		t.Errorf("ip = %q, want %q", ip, "10.212.140.5")
  	}
  }

  func TestParseCharonLineDetectsFailure(t *testing.T) {
  	_, established, failed := parseCharonLine(`establishing CHILD_SA 'oft-tun' failed`)
  	if established || !failed {
  		t.Fatalf("established=%v failed=%v, want established=false failed=true", established, failed)
  	}
  }

  func TestParseCharonLineIgnoresUnrelatedLines(t *testing.T) {
  	ip, established, failed := parseCharonLine("received packet: from 1.2.3.4[500]")
  	if ip != "" || established || failed {
  		t.Errorf("got ip=%q established=%v failed=%v, want all zero/false", ip, established, failed)
  	}
  }

  func TestSwanctlConnFragmentIncludesProfileFields(t *testing.T) {
  	prof := testIPsecProfile()
  	frag := swanctlConnFragment(prof)
  	for _, want := range []string{
  		"oft-tun",
  		prof.Gateway,
  		prof.IPsec.RemoteID,
  		prof.IPsec.IKEProposal,
  		prof.IPsec.ESPProposal,
  		"version = 2", // IKEv2 only
  	} {
  		if !containsString(frag, want) {
  			t.Errorf("swanctl fragment missing %q:\n%s", want, frag)
  		}
  	}
  }

  func testIPsecProfile() config.Profile {
  	return config.Profile{
  		Name:    "Test",
  		Gateway: "vpn.example.com",
  		IPsec: config.IPsecConfig{
  			AuthMethod:  config.IPsecAuthPSK,
  			RemoteID:    "vpn.example.com",
  			IKEProposal: "aes256-sha256-modp2048",
  			ESPProposal: "aes256-sha256-modp2048",
  		},
  	}
  }

  func containsString(haystack, needle string) bool {
  	return strings.Contains(haystack, needle)
  }
  ```

  (add `"strings"` and
  `"github.com/savvaskoualis/openfortitray/internal/config"` to this test
  file's imports)

- [ ] **Step 2: Run the tests to verify they fail**

  Run: `go test ./internal/ipsec/... -run 'TestParseCharonLine|TestSwanctlConnFragment' -v`
  Expected: FAIL to compile (`parseCharonLine`/`swanctlConnFragment` don't
  exist yet).

- [ ] **Step 3: Implement `internal/ipsec/strongswan_unix.go`**

  ```go
  //go:build darwin || linux

  package ipsec

  import (
  	"bufio"
  	"bytes"
  	"context"
  	"fmt"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"regexp"
  	"time"

  	"github.com/savvaskoualis/openfortitray/internal/config"
  )

  // connName is the swanctl connection name this app always uses, so a
  // previous run's fragment is cleanly replaced rather than accumulating
  // stale entries under a name derived from user input.
  const connName = "oft-tun"

  // swanctlDir is where strongSwan expects per-connection config fragments
  // on a Homebrew (macOS) or distro-packaged (Linux) install. Verified
  // against strongSwan's swanctl.conf(5) "conn dir" default.
  const swanctlDir = "/etc/swanctl/conf.d"

  // establishedRe matches charon's CHILD_SA-established log line and
  // captures the assigned (client-side) traffic selector's address.
  // Verified against strongSwan 5.9.x's constants.c log format string.
  var establishedRe = regexp.MustCompile(
  	`CHILD_SA ` + connName + `\{\d+\} established .* TS \S+ === (\d+\.\d+\.\d+\.\d+)`)

  var failedRe = regexp.MustCompile(`establishing CHILD_SA '` + connName + `' failed`)

  // parseCharonLine reports whether line is charon's established-with-IP
  // line, its failed line, or neither.
  func parseCharonLine(line string) (ip string, established, failed bool) {
  	if m := establishedRe.FindStringSubmatch(line); m != nil {
  		return m[1], true, false
  	}
  	if failedRe.MatchString(line) {
  		return "", false, true
  	}
  	return "", false, false
  }

  // swanctlConnFragment renders this profile's swanctl.conf connection
  // block. PSK/cert secrets are written separately by writeSecretsFragment,
  // never interpolated into this fragment.
  func swanctlConnFragment(p config.Profile) string {
  	authLine := "auth = psk"
  	certsLine := ""
  	if p.IPsec.AuthMethod == config.IPsecAuthCert {
  		authLine = "auth = pubkey"
  		certsLine = fmt.Sprintf("\n            certs = %s", p.IPsec.CertPath)
  	}
  	return fmt.Sprintf(`connections {
    %s {
        version = 2
        remote_addrs = %s
        local {
            %s%s
            id = %s
        }
        remote {
            id = %s
        }
        children {
            %s {
                local_ts = 0.0.0.0/0
                remote_ts = 0.0.0.0/0
                esp_proposals = %s
            }
        }
        proposals = %s
    }
}
`, connName, p.Gateway, authLine, certsLine, ipsecLocalID(p), p.IPsec.RemoteID,
		connName, p.IPsec.ESPProposal, p.IPsec.IKEProposal)
  }

  // ipsecLocalID returns the profile's configured local ID, or a sensible
  // default (%any lets charon pick based on the auth method) when unset.
  func ipsecLocalID(p config.Profile) string {
  	if p.IPsec.LocalID != "" {
  		return p.IPsec.LocalID
  	}
  	return "%any"
  }

  // writeSecretsFragment writes the PSK secret (or references the
  // configured cert/key paths) to swanctl's secrets fragment, scoped to
  // this connection and world-unreadable.
  func writeSecretsFragment(p config.Profile, psk string) error {
  	var body string
  	switch p.IPsec.AuthMethod {
  	case config.IPsecAuthCert:
  		body = fmt.Sprintf("private {\n    file = %s\n}\n", p.IPsec.KeyPath)
  	default:
  		body = fmt.Sprintf("ike-%s {\n    id-1 = %s\n    id-2 = %s\n    secret = %q\n}\n",
  			connName, ipsecLocalID(p), p.IPsec.RemoteID, psk)
  	}
  	path := filepath.Join(swanctlDir, connName+".secrets.conf")
  	return os.WriteFile(path, []byte(body), 0o600)
  }

  // NewStrongSwanRunFunc returns the RunFunc that drives strongSwan's
  // swanctl for profile, using psk (ignored unless AuthMethod == IPsecAuthPSK).
  func NewStrongSwanRunFunc(p config.Profile, psk string) RunFunc {
  	return func(ctx context.Context, connected func(ip string)) error {
  		if err := os.MkdirAll(swanctlDir, 0o755); err != nil {
  			return fmt.Errorf("ipsec: creating %s: %w", swanctlDir, err)
  		}
  		connPath := filepath.Join(swanctlDir, connName+".conf")
  		if err := os.WriteFile(connPath, []byte(swanctlConnFragment(p)), 0o644); err != nil {
  			return fmt.Errorf("ipsec: writing swanctl config: %w", err)
  		}
  		if err := writeSecretsFragment(p, psk); err != nil {
  			return fmt.Errorf("ipsec: writing swanctl secrets: %w", err)
  		}
  		defer os.Remove(connPath)
  		defer os.Remove(filepath.Join(swanctlDir, connName+".secrets.conf"))

  		if out, err := exec.CommandContext(ctx, "swanctl", "--load-all").CombinedOutput(); err != nil {
  			return fmt.Errorf("ipsec: swanctl --load-all: %w: %s", err, out)
  		}

  		initCtx, cancelInit := context.WithCancel(ctx)
  		defer cancelInit()
  		cmd := exec.CommandContext(initCtx, "swanctl", "--initiate", "-c", connName, "--log-level", "2")
  		stdout, err := cmd.StdoutPipe()
  		if err != nil {
  			return fmt.Errorf("ipsec: swanctl --initiate stdout pipe: %w", err)
  		}
  		cmd.Stderr = cmd.Stdout
  		if err := cmd.Start(); err != nil {
  			return fmt.Errorf("ipsec: starting swanctl --initiate: %w", err)
  		}

  		established := false
  		scanner := bufio.NewScanner(stdout)
  		for scanner.Scan() {
  			line := scanner.Text()
  			ip, ok, failed := parseCharonLine(line)
  			if ok {
  				established = true
  				connected(ip)
  				break // swanctl --initiate is one-shot; charon keeps the SA up as a daemon from here — health is polled below, not read from this exited process
  			}
  			if failed {
  				break
  			}
  		}

  		waitErr := cmd.Wait()
  		if ctx.Err() != nil {
  			terminateCmd := exec.Command("swanctl", "--terminate", "-c", connName)
  			_ = terminateCmd.Run()
  			return ctx.Err()
  		}
  		if !established {
  			if waitErr != nil {
  				return fmt.Errorf("ipsec: swanctl --initiate: %w", waitErr)
  			}
  			return fmt.Errorf("ipsec: swanctl --initiate exited without establishing the tunnel")
  		}

  		// swanctl --initiate has already exited (it's a one-shot trigger, not a
  		// long-running process) — charon keeps the SA up as its own background
  		// daemon from here. Poll --list-sas for as long as the SA is still
  		// installed; a drop (DPD timeout, gateway-initiated teardown) shows up
  		// as the connection name disappearing from the list, which is the
  		// signal to return an error so the Supervisor's backoff/reconnect loop
  		// (Task 2) takes over — without this poll, a drop after a successful
  		// connect would never be noticed at all.
  		ticker := time.NewTicker(5 * time.Second)
  		defer ticker.Stop()
  		for {
  			select {
  			case <-ctx.Done():
  				terminateCmd := exec.Command("swanctl", "--terminate", "-c", connName)
  				_ = terminateCmd.Run()
  				return ctx.Err()
  			case <-ticker.C:
  				out, err := exec.CommandContext(ctx, "swanctl", "--list-sas", "-c", connName).CombinedOutput()
  				if err != nil || !bytes.Contains(out, []byte(connName)) {
  					return fmt.Errorf("ipsec: %s SA no longer installed", connName)
  				}
  			}
  		}
  	}
  }
  ```

- [ ] **Step 4: Run the tests to verify they pass**

  Run: `go test ./internal/ipsec/... -v`
  Expected: all PASS, including Task 2's tests (still green).

- [ ] **Step 5: Run the full darwin build/vet/test**

  Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
  Expected: all PASS. `gofmt -l .` empty.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/ipsec/strongswan_unix.go internal/ipsec/strongswan_unix_test.go
  git commit -m "feat(ipsec): strongSwan-backed IPsec runtime for macOS and Linux"
  ```

---

### Task 4: Native Windows IKEv2 runtime

**Files:**
- Create: `internal/ipsec/ipsec_windows.go`
- Test: `internal/ipsec/ipsec_windows_test.go`

**Interfaces:**
- Consumes: `config.Profile`, `config.IPsecConfig`.
- Produces: `NewWindowsRunFunc(profile config.Profile, psk string) RunFunc`
  — the `RunFunc` Task 6 passes to `ipsec.New` when `runtime.GOOS ==
  "windows"`.

**Verification note:** this task's behavior CANNOT be tested on this
machine (macOS, no Windows). Use the scratch-module cross-compile
technique already established in this project (`glass_windows.go`'s task):
copy the new file into a throwaway module with `package scratch`, run
`GOOS=windows GOARCH=amd64 go build ./...` there to verify syntax/types,
then delete the scratch module. State plainly in the report that this is
syntax-verification only.

- [ ] **Step 1: Write the failing tests for the pure parsing/rendering pieces**

  `Get-VpnConnection`'s `ConnectionStatus` values are `Disconnected`,
  `Connecting`, `Connected` (verified against Microsoft's `VpnConnection`
  cmdlet documentation). `Get-VpnConnection ... | Select
  -ExpandProperty ConnectionStatus` piped through `-Format List` style
  output is what this task parses.

  In `internal/ipsec/ipsec_windows_test.go`:

  ```go
  package ipsec

  import "testing"

  func TestParseVpnConnectionStatusRecognizesEachState(t *testing.T) {
  	cases := map[string]vpnStatus{
  		"Connected":    vpnConnected,
  		"Connecting":   vpnConnecting,
  		"Disconnected": vpnDisconnected,
  	}
  	for input, want := range cases {
  		if got := parseVpnConnectionStatus(input); got != want {
  			t.Errorf("parseVpnConnectionStatus(%q) = %v, want %v", input, got, want)
  		}
  	}
  }

  func TestParseVpnConnectionStatusUnknownIsDisconnected(t *testing.T) {
  	if got := parseVpnConnectionStatus("SomethingNew"); got != vpnDisconnected {
  		t.Errorf("unknown status = %v, want vpnDisconnected (fail closed, never hang assuming connected)", got)
  	}
  }

  func TestAddVpnConnectionArgsIncludeProfileFields(t *testing.T) {
  	prof := testIPsecProfile()
  	args := addVpnConnectionArgs(prof)
  	for _, want := range []string{
  		vpnConnectionName,
  		"-ServerAddress", prof.Gateway,
  		"-TunnelType", "IKEv2",
  		"-AuthenticationMethod",
  	} {
  		if !argsContain(args, want) {
  			t.Errorf("Add-VpnConnection args missing %q: %v", want, args)
  		}
  	}
  }

  func argsContain(args []string, want string) bool {
  	for _, a := range args {
  		if a == want {
  			return true
  		}
  	}
  	return false
  }
  ```

- [ ] **Step 2: Run the tests to verify they fail**

  Cross-compile-verify only (see the verification note above) — copy
  `ipsec_windows_test.go` alongside `ipsec_windows.go` into the scratch
  module and confirm the test file itself references undefined symbols
  (`parseVpnConnectionStatus`, `vpnStatus`, etc.) before Step 3.

- [ ] **Step 3: Implement `internal/ipsec/ipsec_windows.go`**

  ```go
  //go:build windows

  package ipsec

  import (
  	"context"
  	"fmt"
  	"os/exec"
  	"strings"
  	"time"

  	"github.com/savvaskoualis/openfortitray/internal/config"
  )

  // vpnConnectionName is the Windows VPN connection profile name this app
  // always uses, so re-Connecting cleanly replaces any previous profile
  // rather than accumulating stale ones under a name derived from user input.
  const vpnConnectionName = "OpenFortiTray IPsec"

  type vpnStatus int

  const (
  	vpnDisconnected vpnStatus = iota
  	vpnConnecting
  	vpnConnected
  )

  // parseVpnConnectionStatus maps Get-VpnConnection's ConnectionStatus text
  // (verified against Microsoft's VpnConnection cmdlet docs: Connected,
  // Connecting, Disconnected, Disconnecting) onto vpnStatus. Anything
  // unrecognized — including "Disconnecting" — reports vpnDisconnected:
  // failing closed means a wedged/renamed state is treated as "not up" and
  // retried, never mistaken for a live tunnel.
  func parseVpnConnectionStatus(s string) vpnStatus {
  	switch strings.TrimSpace(s) {
  	case "Connected":
  		return vpnConnected
  	case "Connecting":
  		return vpnConnecting
  	default:
  		return vpnDisconnected
  	}
  }

  // addVpnConnectionArgs builds Add-VpnConnection's argument list for p.
  // PSK/cert secrets are passed via a separate secured-string argument in
  // runPowerShell, never interpolated into this slice as plain text.
  func addVpnConnectionArgs(p config.Profile) []string {
  	authMethod := "PSK"
  	if p.IPsec.AuthMethod == config.IPsecAuthCert {
  		authMethod = "MachineCertificate"
  	}
  	return []string{
  		"-Name", vpnConnectionName,
  		"-ServerAddress", p.Gateway,
  		"-TunnelType", "IKEv2",
  		"-AuthenticationMethod", authMethod,
  		"-EncryptionLevel", "Required",
  		"-Force",
  	}
  }

  // runPowerShell runs a PowerShell command with args, returning combined
  // output. Every call here is best-effort: callers log and degrade rather
  // than panic on a missing/broken PowerShell — see NewWindowsRunFunc.
  func runPowerShell(ctx context.Context, args ...string) (string, error) {
  	full := append([]string{"-NoProfile", "-NonInteractive", "-Command"}, args...)
  	out, err := exec.CommandContext(ctx, "powershell.exe", full...).CombinedOutput()
  	return string(out), err
  }

  // NewWindowsRunFunc returns the RunFunc that drives Windows' native IKEv2
  // VPN stack for profile, using psk (ignored unless AuthMethod ==
  // IPsecAuthPSK).
  func NewWindowsRunFunc(p config.Profile, psk string) RunFunc {
  	return func(ctx context.Context, connected func(ip string)) error {
  		addArgs := addVpnConnectionArgs(p)
  		if p.IPsec.AuthMethod == config.IPsecAuthPSK {
  			addArgs = append(addArgs, "-L2tpPsk", psk)
  		}
  		cmdline := fmt.Sprintf("Remove-VpnConnection -Name %q -Force -ErrorAction SilentlyContinue; Add-VpnConnection %s",
  			vpnConnectionName, strings.Join(quoteArgs(addArgs), " "))
  		if out, err := runPowerShell(ctx, cmdline); err != nil {
  			return fmt.Errorf("ipsec: Add-VpnConnection: %w: %s", err, out)
  		}

  		if out, err := runPowerShell(ctx, fmt.Sprintf("rasdial %q", vpnConnectionName)); err != nil {
  			return fmt.Errorf("ipsec: rasdial: %w: %s", err, out)
  		}

  		poll := time.NewTicker(2 * time.Second)
  		defer poll.Stop()
  		reportedConnected := false
  		for {
  			select {
  			case <-ctx.Done():
  				_, _ = runPowerShell(context.Background(),
  					fmt.Sprintf("rasdial %q /disconnect", vpnConnectionName))
  				return ctx.Err()
  			case <-poll.C:
  				out, err := runPowerShell(ctx, fmt.Sprintf(
  					"(Get-VpnConnection -Name %q).ConnectionStatus", vpnConnectionName))
  				if err != nil {
  					return fmt.Errorf("ipsec: Get-VpnConnection: %w: %s", err, out)
  				}
  				switch parseVpnConnectionStatus(out) {
  				case vpnConnected:
  					if !reportedConnected {
  						reportedConnected = true
  						ip, _ := runPowerShell(ctx, fmt.Sprintf(
  							"(Get-VpnConnection -Name %q).ClientIPAddress", vpnConnectionName))
  						connected(strings.TrimSpace(ip))
  					}
  				case vpnDisconnected:
  					if reportedConnected {
  						return fmt.Errorf("ipsec: VPN connection dropped")
  					}
  				}
  			}
  		}
  	}
  }

  // quoteArgs wraps each arg in double quotes for interpolation into a
  // PowerShell command line built via -Command; values here are either
  // fixed strings (see addVpnConnectionArgs) or the profile's own Gateway,
  // never raw user free-text that could break out of the quoting.
  func quoteArgs(args []string) []string {
  	out := make([]string, len(args))
  	for i, a := range args {
  		out[i] = fmt.Sprintf("%q", a)
  	}
  	return out
  }
  ```

- [ ] **Step 4: Cross-compile verify**

  ```bash
  mkdir -p /tmp/ipsec-windows-scratch
  cp internal/ipsec/ipsec_windows.go internal/ipsec/ipsec_windows_test.go /tmp/ipsec-windows-scratch/
  cd /tmp/ipsec-windows-scratch
  sed -i '' 's/^package ipsec$/package scratch/' ipsec_windows.go ipsec_windows_test.go
  # minimal go.mod + a stub config package matching the two Profile fields
  # this file touches (Gateway, IPsec.AuthMethod/CertPath) — see the Task 3
  # Windows precedent (glass_windows.go) for the exact scratch-module shape.
  GOOS=windows GOARCH=amd64 go build ./...
  cd /
  rm -rf /tmp/ipsec-windows-scratch
  ```

  Expected: builds clean. Report the actual command and output; do not
  claim success without having run it.

- [ ] **Step 5: Run the full darwin build/vet/test**

  Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
  Expected: PASS — `ipsec_windows.go`/`ipsec_windows_test.go` are
  `//go:build windows`-tagged so darwin doesn't compile them at all;
  confirm the rest of the repo is unaffected.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/ipsec/ipsec_windows.go internal/ipsec/ipsec_windows_test.go
  git commit -m "feat(ipsec): native Windows IKEv2 runtime"
  ```

---

### Task 5: Settings UI

**Files:**
- Modify: `internal/settings/logic.go`
- Modify: `internal/settings/settings.go`
- Test: `internal/settings/logic_test.go`

**Interfaces:**
- Consumes: `config.IPsecConfig`, `config.IPsecAuthMethod`.
- Produces: `FieldIPsecAuthMethod`, `FieldIPsecSecret`,
  `FieldIPsecCertPath`, `FieldIPsecKeyPath` (new `Field*` constants),
  `validateIPsecFieldsPresent(c *config.Config) error`, IPsec auth-method
  Select + PSK/cert fields in Connection, IKE/ESP proposal + Local/Remote
  ID fields in Advanced.

- [ ] **Step 1: Add the new Field constants and pure validators to `logic.go`**

  ```go
  	FieldServerCert    = "servercert"
  	FieldSplitDNS      = "splitdns"
  	FieldIPsecAuth     = "ipsecauth"
  	FieldIPsecSecret   = "ipsecsecret"
  	FieldIPsecCertPath = "ipseccertpath"
  	FieldIPsecKeyPath  = "ipseckeypath"
  ```

  ```go
  // ipsecAuthLabels is the IPsec Auth method Select's option list.
  var ipsecAuthLabels = []string{ipsecPSKLabel, ipsecCertLabel}

  const (
  	ipsecPSKLabel  = "Pre-shared key"
  	ipsecCertLabel = "Certificate"
  )

  func ipsecAuthLabel(m config.IPsecAuthMethod) string {
  	if m == config.IPsecAuthCert {
  		return ipsecCertLabel
  	}
  	return ipsecPSKLabel
  }

  func ipsecAuthFromLabel(label string) config.IPsecAuthMethod {
  	if label == ipsecCertLabel {
  		return config.IPsecAuthCert
  	}
  	return config.IPsecAuthPSK
  }

  // validateIPsecFieldsPresent reports the first missing field an IPsec
  // profile needs for its chosen auth method — a PSK secret (checked by the
  // caller, since the secret lives in credstore, not this struct) or a
  // cert+key path pair. Only meaningful when Backend == BackendIPsec; the
  // caller gates on that.
  func validateIPsecFieldsPresent(ic config.IPsecConfig) (field, message string) {
  	if ic.AuthMethod == config.IPsecAuthCert {
  		if ic.CertPath == "" {
  			return FieldIPsecCertPath, "Choose a client certificate file in Basic ▸ Certificate."
  		}
  		if ic.KeyPath == "" {
  			return FieldIPsecKeyPath, "Choose a private key file in Basic ▸ Private key."
  		}
  	}
  	return "", ""
  }
  ```

- [ ] **Step 2: Write the failing tests**

  In `internal/settings/logic_test.go`:

  ```go
  func TestIPsecAuthLabelRoundTrip(t *testing.T) {
  	for _, m := range []config.IPsecAuthMethod{config.IPsecAuthPSK, config.IPsecAuthCert} {
  		if got := ipsecAuthFromLabel(ipsecAuthLabel(m)); got != m {
  			t.Errorf("round trip: %q -> %q -> %q", m, ipsecAuthLabel(m), got)
  		}
  	}
  }

  func TestValidateIPsecFieldsPresentPSKNeedsNothingHere(t *testing.T) {
  	field, msg := validateIPsecFieldsPresent(config.IPsecConfig{AuthMethod: config.IPsecAuthPSK})
  	if field != "" || msg != "" {
  		t.Errorf("PSK: got field=%q msg=%q, want both empty (secret lives in credstore, checked elsewhere)", field, msg)
  	}
  }

  func TestValidateIPsecFieldsPresentCertRequiresCertAndKey(t *testing.T) {
  	field, _ := validateIPsecFieldsPresent(config.IPsecConfig{AuthMethod: config.IPsecAuthCert})
  	if field != FieldIPsecCertPath {
  		t.Errorf("no cert/key set: field = %q, want %q", field, FieldIPsecCertPath)
  	}
  	field, _ = validateIPsecFieldsPresent(config.IPsecConfig{
  		AuthMethod: config.IPsecAuthCert, CertPath: "/x.crt"})
  	if field != FieldIPsecKeyPath {
  		t.Errorf("cert set, no key: field = %q, want %q", field, FieldIPsecKeyPath)
  	}
  	field, _ = validateIPsecFieldsPresent(config.IPsecConfig{
  		AuthMethod: config.IPsecAuthCert, CertPath: "/x.crt", KeyPath: "/x.key"})
  	if field != "" {
  		t.Errorf("both set: field = %q, want empty", field)
  	}
  }
  ```

- [ ] **Step 3: Run the tests to verify they fail, then pass**

  Run: `go test ./internal/settings/... -run 'TestIPsecAuthLabelRoundTrip|TestValidateIPsecFieldsPresent' -v`
  Expected: FAIL to compile until Step 1 lands, then PASS.

- [ ] **Step 4: Remove `validateBackendSupported`'s refusal**

  `validateBackendSupported`'s own doc comment already says "When IPsec
  ships, delete this function and its two call sites below." Do exactly
  that: delete `validateBackendSupported` from `logic.go`, and its two
  call sites — one in `Save`'s validator chain, one in `FirstConnectIssue`
  (the block reading `// Backend: only SSL is wired into the runtime
  today.`). Delete `backendNoteText` and its call site
  (`updateBackendNote` in `settings.go`) the same way — the Protocol
  Select no longer needs a warning note once IPsec actually works. Delete
  `c.backendNote`/`c.backendNoteRow` fields and their construction in
  `settings.go`, and the `Protocol` FormItem's neighboring note row in the
  `Connection` group (keep the `Protocol` FormItem itself).

- [ ] **Step 5: Add the IPsec fields to the Connection section (`settings.go`)**

  Near `c.backendSelect`'s construction, add:

  ```go
  	c.ipsecAuthSelect = widget.NewSelect(ipsecAuthLabels, func(label string) {
  		if c.loading {
  			return
  		}
  		c.work.Profiles[c.sel].IPsec.AuthMethod = ipsecAuthFromLabel(label)
  		c.updateIPsecAuthVisibility()
  	})

  	c.ipsecSecretEntry = widget.NewPasswordEntry()
  	c.ipsecSecretEntry.OnChanged = func(v string) {
  		if c.loading {
  			return
  		}
  		c.ipsecSecretDirty = true
  		c.ipsecSecretValue = v
  	}

  	c.ipsecCertPathLabel = widget.NewLabel("")
  	c.ipsecCertPathButton = widget.NewButton("Choose certificate…", func() {
  		dialog.ShowFileOpen(func(f fyne.URIReadCloser, err error) {
  			if err != nil || f == nil {
  				return
  			}
  			defer f.Close()
  			c.work.Profiles[c.sel].IPsec.CertPath = f.URI().Path()
  			c.ipsecCertPathLabel.SetText(f.URI().Path())
  		}, c.win)
  	})

  	c.ipsecKeyPathLabel = widget.NewLabel("")
  	c.ipsecKeyPathButton = widget.NewButton("Choose private key…", func() {
  		dialog.ShowFileOpen(func(f fyne.URIReadCloser, err error) {
  			if err != nil || f == nil {
  				return
  			}
  			defer f.Close()
  			c.work.Profiles[c.sel].IPsec.KeyPath = f.URI().Path()
  			c.ipsecKeyPathLabel.SetText(f.URI().Path())
  		}, c.win)
  	})

  	c.ipsecPSKRow = c.row("Pre-shared key", c.ipsecSecretEntry)
  	c.ipsecCertRow = c.row("Certificate", container.NewHBox(c.ipsecCertPathButton, c.ipsecCertPathLabel))
  	c.ipsecKeyRow = c.row("Private key", container.NewHBox(c.ipsecKeyPathButton, c.ipsecKeyPathLabel))
  ```

  Add `ipsecAuthSelect *widget.Select`, `ipsecSecretEntry *widget.Entry`,
  `ipsecSecretDirty bool`, `ipsecSecretValue string`,
  `ipsecCertPathLabel, ipsecKeyPathLabel *widget.Label`,
  `ipsecCertPathButton, ipsecKeyPathButton *widget.Button`,
  `ipsecPSKRow, ipsecCertRow, ipsecKeyRow *fyne.Container` to the
  `Controller` struct. Add `"IPsec auth"` and the cert/key rows into the
  `Connection` group (via `sections`), right after the `Protocol`
  FormItem, following the same "form item + rows below it" pattern the
  removed `backendNoteRow` used — a `widget.NewFormItem("IPsec auth",
  c.ipsecAuthSelect)` folded into `connectionForm`, and
  `c.ipsecPSKRow`/`c.ipsecCertRow`/`c.ipsecKeyRow` passed to `c.group(...)`
  the same way `c.backendNoteRow` was.

  Add `updateIPsecAuthVisibility` (mirrors `updateAuthNote`'s
  show/hide-by-toggling-a-row shape): shows `ipsecPSKRow` XOR
  (`ipsecCertRow` + `ipsecKeyRow`) based on
  `c.work.Profiles[c.sel].IPsec.AuthMethod`, and additionally hides all
  three rows entirely when `c.work.Profiles[c.sel].Backend !=
  config.BackendIPsec` (an SSL profile has no reason to show any IPsec
  field) — call it from both the `backendSelect`/`ipsecAuthSelect`
  callbacks and from `loadProfile`, the same call sites
  `updateBackendNote` used to be wired from.

- [ ] **Step 6: Add the Advanced-tab IPsec fields**

  In whichever function builds `c.advanced` (the Advanced tab's content),
  add form entries for `IKEProposal`, `ESPProposal`, `LocalID`, `RemoteID`
  bound the same way the tab's other advanced text fields already are
  (`widget.NewEntry()` + `OnChanged` writing into
  `c.work.Profiles[c.sel].IPsec.<Field>`), grouped under a new `"IPsec"`
  caption via `c.group("IPsec", ...)` alongside the tab's existing groups.
  These four fields are always visible on the Advanced tab regardless of
  `Backend` — they're inert (never read) for an SSL profile, matching how
  every other forward-designed-but-inactive field in this app already
  behaves (e.g. `Auth.CertPath` for the not-yet-implemented `AuthCert`
  method).

- [ ] **Step 7: Wire `loadProfile`/`Save` to round-trip the IPsec fields**

  In `loadProfile`, set `c.ipsecAuthSelect.SetSelected(...)`, the four
  Advanced entries' text, and reset `c.ipsecSecretDirty = false` /
  `c.ipsecSecretEntry.SetText("")` (never pre-fill a secret field with a
  stored value — same convention the SSL password field already follows),
  then call `c.updateIPsecAuthVisibility()`.

  In `Save`, after the existing credential-write step for the SSL
  password/cookie, add: if `c.work.Profiles[c.sel].Backend ==
  config.BackendIPsec && c.work.Profiles[c.sel].IPsec.AuthMethod ==
  config.IPsecAuthPSK && c.ipsecSecretDirty`, call
  `credstore.Set(config.IPsecPSKCredstoreKey(profile.Gateway),
  c.ipsecSecretValue)` and reset `c.ipsecSecretDirty = false`.

- [ ] **Step 8: Add the new field check to `FirstConnectIssue`**

  Right after the existing `// Authentication: ...` block, before the
  final `return nil`:

  ```go
  	// IPsec fields: only meaningful when Backend == BackendIPsec.
  	if prof.Backend == config.BackendIPsec {
  		if field, msg := validateIPsecFieldsPresent(prof.IPsec); field != "" {
  			return &Issue{name, TabBasic, field, msg}
  		}
  	}
  ```

- [ ] **Step 9: Run the full settings suite**

  Run: `go test ./internal/settings/... -v`
  Expected: all PASS, including every pre-existing test — in particular,
  confirm no existing test asserted on `backendNoteText`/
  `validateBackendSupported`/`c.backendNote` still existing (update any
  that did, since Step 4 removed them).

- [ ] **Step 10: Run the full darwin build/vet/test**

  Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
  Expected: PASS. `gofmt -l .` empty.

- [ ] **Step 11: Commit**

  ```bash
  git add internal/settings/logic.go internal/settings/settings.go internal/settings/logic_test.go
  git commit -m "feat(settings): real IPsec fields (PSK/cert, proposals), drop the refusal"
  ```

---

### Task 6: Wire into `cmd/openfortitray`

**Files:**
- Modify: `cmd/openfortitray/main.go`

**Interfaces:**
- Consumes: `ipsec.New`, `ipsec.NewStrongSwanRunFunc` /
  `ipsec.NewWindowsRunFunc` (build-tag-selected), `config.IPsecPSKCredstoreKey`.

- [ ] **Step 1: Add a per-OS constructor for the IPsec RunFunc**

  Create `cmd/openfortitray/ipsecrun_unix.go` (`//go:build darwin ||
  linux`) and `cmd/openfortitray/ipsecrun_windows.go` (`//go:build
  windows`), each exporting the same signature so `main.go` needs no
  build tags of its own:

  ```go
  //go:build darwin || linux

  package main

  import (
  	"github.com/savvaskoualis/openfortitray/internal/config"
  	"github.com/savvaskoualis/openfortitray/internal/ipsec"
  )

  func newIPsecRunFunc(p config.Profile, psk string) ipsec.RunFunc {
  	return ipsec.NewStrongSwanRunFunc(p, psk)
  }
  ```

  ```go
  //go:build windows

  package main

  import (
  	"github.com/savvaskoualis/openfortitray/internal/config"
  	"github.com/savvaskoualis/openfortitray/internal/ipsec"
  )

  func newIPsecRunFunc(p config.Profile, psk string) ipsec.RunFunc {
  	return ipsec.NewWindowsRunFunc(p, psk)
  }
  ```

- [ ] **Step 2: Dispatch by `Backend` in `startTunnel`**

  Find `startTunnel` (the function that currently always builds/uses
  `a.sup`, an `internal/tunnel.Supervisor`). Change it to construct the
  right supervisor for the active profile's `Backend` — SSL keeps its
  existing `a.sup` (`*tunnel.Supervisor`) path unchanged; IPsec constructs
  an `*ipsec.Supervisor` sharing the same `a.events` channel:

  ```go
  func (a *app) startTunnel() {
  	prof := a.cfg.Active()
  	if prof.Backend == config.BackendIPsec {
  		psk, err := credstore.Get(config.IPsecPSKCredstoreKey(prof.Gateway))
  		if err != nil {
  			log.Printf("ipsec: reading PSK from credstore: %v", err)
  		}
  		ipsecSup := ipsec.New(newIPsecRunFunc(prof, psk), a.events)
  		ipsecSup.SetKeepAlive(prof.KeepAlive)
  		a.sup = ipsecSup
  		a.wantConnected.Store(true)
  		a.sup.Connect()
  		return
  	}
  	// existing SSL path unchanged below this line
  	...
  }
  ```

  This requires `a.sup`'s field type to already be the `supervisor`
  interface (it is — see `cmd/openfortitray/main.go:51`), so assigning
  either a `*tunnel.Supervisor` or an `*ipsec.Supervisor` to it needs no
  other change. Add `"github.com/savvaskoualis/openfortitray/internal/ipsec"`
  and `"github.com/savvaskoualis/openfortitray/internal/credstore"` (if not
  already imported) to `main.go`.

- [ ] **Step 3: Write the test**

  In `cmd/openfortitray/main_test.go`, add a fake matching the existing
  `fakeSupervisor` test-double pattern is not needed here — this is
  integration-level: confirm `startTunnel` picks the IPsec path without
  needing a real strongSwan/Windows install, by having the test construct
  an `app` with `cfg.Profiles[0].Backend = config.BackendIPsec` and
  asserting `a.sup` is an `*ipsec.Supervisor` (a type assertion), not that
  it actually connects:

  ```go
  func TestStartTunnelUsesIPsecSupervisorForIPsecBackend(t *testing.T) {
  	a, _ := newTestApp(t, "vpn.example.com", t.TempDir())
  	a.cfg.Profiles[0].Backend = config.BackendIPsec
  	a.startTunnel()
  	defer a.sup.Disconnect()

  	if _, ok := a.sup.(*ipsec.Supervisor); !ok {
  		t.Errorf("a.sup is %T, want *ipsec.Supervisor for an IPsec-backend profile", a.sup)
  	}
  }
  ```

  Add `"github.com/savvaskoualis/openfortitray/internal/ipsec"` to
  `main_test.go`'s imports.

- [ ] **Step 4: Run the test to verify it fails, then implement, then passes**

  Run: `go test ./cmd/openfortitray/... -run TestStartTunnelUsesIPsecSupervisorForIPsecBackend -v`
  Expected: FAIL (compile error or `a.sup` still `*tunnel.Supervisor`)
  before Steps 1-2, PASS after.

- [ ] **Step 5: Run the full darwin build/vet/test**

  Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
  Expected: PASS. `gofmt -l .` empty.

- [ ] **Step 6: Commit**

  ```bash
  git add cmd/openfortitray/ipsecrun_unix.go cmd/openfortitray/ipsecrun_windows.go cmd/openfortitray/main.go cmd/openfortitray/main_test.go
  git commit -m "feat: wire the IPsec backend into startTunnel"
  ```

---

## Self-Review

**Spec coverage:** every section of the design doc has a task: config data
model (Task 1), the platform-agnostic supervision loop (Task 2), the
strongSwan runtime (Task 3), the native Windows runtime (Task 4), Settings
UX — Basic/Advanced split, progressive disclosure, file pickers, validation,
removing the old refusal (Task 5), and wiring it all into the app (Task 6).

**Placeholder scan:** none found — every step's code is complete and used.
(An earlier draft of Task 3 had a dead-code `_ = localAuthExtra` var and a
throwaway `var _ = strings.TrimSpace` unused-import silencer; both were caught
in self-review and fixed: the cert-auth `certs =` line is now actually
interpolated into the swanctl fragment, and the unused `strings` import was
dropped rather than silenced.)

**Type consistency:** `ipsec.RunFunc` (`func(ctx context.Context, connected
func(ip string)) error`), defined in Task 2, is the exact signature Task 3's
`NewStrongSwanRunFunc` and Task 4's `NewWindowsRunFunc` both return, and the
exact signature Task 6's `newIPsecRunFunc` (both platform variants) returns.
`config.IPsecPSKCredstoreKey` (Task 1) is the exact key both Task 5's Save
path and Task 6's `startTunnel` use.

## Verification honesty (read before claiming this plan is "done")

Tasks 1, 2, 3, 5, and 6 are fully verifiable on this machine — build, test,
and (for Task 3's strongSwan path specifically) real behavior against a
Homebrew-installed strongSwan. Task 4's Windows runtime is grounded in
Microsoft's documented `VpnConnection` cmdlet behavior but is NOT
behaviorally verified — nobody has run it against a real Windows IKEv2
connection. Say so plainly when reporting on it; do not let "cross-compiles
clean" read as "confirmed working." The strongSwan config-file paths and
log-format assumptions in Task 3 are grounded in strongSwan's documented
`swanctl.conf(5)` format and 5.9.x log output, but the FIRST real Connect
attempt against a live gateway is still the actual test — flag clearly if
that hasn't happened yet when this plan is reported done.
