# Hyperio VPN Tray App Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Single Go binary `hyp-vpn` with a tray icon that connects to the company FortiGate SSL-VPN (SAML external-browser auth) via OpenConnect, auto-starts at login, and auto-reconnects — on macOS, Linux, and Windows.

**Architecture:** Tray UI (`fyne.io/systray`) drives a tunnel supervisor that obtains an `SVPNCOOKIE` through a localhost SAML redirect flow and supervises an `openconnect --protocol=fortinet` child process with backoff restart. Autostart is a per-OS login item. All I/O boundaries (browser open, HTTP client, tunnel process) are injected so core logic is unit-testable.

**Tech Stack:** Go ≥1.22, `fyne.io/systray`, stdlib `net/http`, OpenConnect ≥9.x as external binary.

## Global Constraints

- Gateway default: `securityhub.hyperio.cloud`, port `10443`, SAML listen port `8020` (from spec).
- Module path: `github.com/hyperiosoftware/hyp-vpn`.
- Go ≥1.22; only external Go dependency allowed: `fyne.io/systray` (plus its transitive deps).
- The app never implements tunneling itself; it always spawns openconnect.
- All commits: message prefix `feat:`/`fix:`/`chore:`/`docs:`, body optional.
- Repo root: `~/code/hyp-vpn` (already a git repo, spec committed).
- Tests: `go test ./...` must pass on macOS (dev machine). No network access in tests.

---

### Task 1: Module scaffold + config package

**Files:**
- Create: `go.mod`, `.gitignore`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  ```go
  package config
  type Config struct {
      Gateway         string `json:"gateway"`
      Port            int    `json:"port"`
      SAMLPort        int    `json:"saml_port"`
      OpenconnectPath string `json:"openconnect_path"`
      Autostart       bool   `json:"autostart"`
  }
  func Load(dir string) (*Config, error)   // defaults overlaid with dir/config.json if present
  func (c *Config) Save(dir string) error  // writes dir/config.json (0600), creates dir
  func DefaultDir() (string, error)        // os.UserConfigDir() + "/hyp-vpn"
  func (c *Config) GatewayURL() string     // "https://" + Gateway + ":" + Port
  ```

- [ ] **Step 1: Scaffold module**

```bash
cd ~/code/hyp-vpn
go mod init github.com/hyperiosoftware/hyp-vpn
printf 'dist/\n*.log\n' > .gitignore
```

- [ ] **Step 2: Write failing tests**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(t.TempDir()) // no config.json present
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway != "securityhub.hyperio.cloud" || c.Port != 10443 || c.SAMLPort != 8020 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.OpenconnectPath != "openconnect" {
		t.Fatalf("default openconnect path should be bare name, got %q", c.OpenconnectPath)
	}
	if !c.Autostart {
		t.Fatal("autostart should default to true")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(dir)
	c.Gateway = "other.example.com"
	c.Autostart = false
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Gateway != "other.example.com" || c2.Autostart {
		t.Fatalf("round trip lost data: %+v", c2)
	}
}

func TestLoadOverlayKeepsUnsetDefaults(t *testing.T) {
	dir := t.TempDir()
	// partial file: only gateway set
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"gateway":"x.example.com"}`), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway != "x.example.com" || c.Port != 10443 {
		t.Fatalf("overlay broken: %+v", c)
	}
}

func TestGatewayURL(t *testing.T) {
	c, _ := Load(t.TempDir())
	if got := c.GatewayURL(); got != "https://securityhub.hyperio.cloud:10443" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 3: Run tests, verify fail**

Run: `go test ./internal/config/`
Expected: FAIL (package missing / undefined symbols)

- [ ] **Step 4: Implement**

`internal/config/config.go`:

```go
// Package config holds static VPN settings and user preferences.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Gateway         string `json:"gateway"`
	Port            int    `json:"port"`
	SAMLPort        int    `json:"saml_port"`
	OpenconnectPath string `json:"openconnect_path"`
	Autostart       bool   `json:"autostart"`
}

func defaults() *Config {
	return &Config{
		Gateway:         "securityhub.hyperio.cloud",
		Port:            10443,
		SAMLPort:        8020,
		OpenconnectPath: "openconnect",
		Autostart:       true,
	}
}

// Load returns defaults overlaid with dir/config.json when the file exists.
func Load(dir string) (*Config, error) {
	c := defaults()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return c, nil
}

func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "hyp-vpn"), nil
}

func (c *Config) GatewayURL() string {
	return fmt.Sprintf("https://%s:%d", c.Gateway, c.Port)
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod .gitignore internal/config/
git commit -m "feat: config package with defaults, overlay load, save"
```

---

### Task 2: SAML external-browser auth package

**Files:**
- Create: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `config.Config.GatewayURL()` (string like `https://host:port`)
- Produces:
  ```go
  package auth
  type Authenticator struct {
      GatewayURL  string            // e.g. https://securityhub.hyperio.cloud:10443
      ListenPort  int               // e.g. 8020
      Client      *http.Client      // nil → http.DefaultClient
      OpenBrowser func(url string) error
  }
  // Authenticate runs the Fortinet SAML external-browser flow and returns
  // the SVPNCOOKIE value. Blocks until login completes, ctx cancels, or times out.
  func (a *Authenticator) Authenticate(ctx context.Context) (string, error)
  func SystemOpenBrowser(url string) error // exec open/xdg-open/rundll32 per GOOS
  ```

**Flow being implemented (from spec):** listen on `127.0.0.1:ListenPort`; open browser at `GatewayURL + "/remote/saml/start?redirect=1"`; browser eventually hits `http://127.0.0.1:port/?id=<auth-id>`; app then GETs `GatewayURL + "/remote/saml/auth_id?id=<auth-id>"` and reads the `SVPNCOOKIE` cookie from the response; responds to the browser with a "you can close this tab" page.

- [ ] **Step 1: Write failing tests**

`internal/auth/auth_test.go`:

```go
package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGateway emulates the FortiGate SAML endpoints.
func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/remote/saml/auth_id", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "AUTH123" {
			http.Error(w, "bad id", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: "COOKIEVALUE"})
	})
	return httptest.NewTLSServer(mux)
}

func TestAuthenticateHappyPath(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()

	a := &Authenticator{
		GatewayURL: gw.URL,
		ListenPort: 0, // pick free port; Authenticate must support 0 for tests
		Client:     gw.Client(),
		// Fake browser: immediately performs the redirect the FortiGate would trigger.
		OpenBrowser: nil, // set below, needs the listen address
	}
	a.OpenBrowser = func(loginURL string) error {
		if !strings.HasPrefix(loginURL, gw.URL+"/remote/saml/start?redirect=1") {
			t.Errorf("wrong login URL: %s", loginURL)
		}
		go func() {
			// FortiGate redirects the browser to the local listener with the auth id.
			c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			for i := 0; i < 50; i++ { // wait for listener
				_, err := c.Get(fmt.Sprintf("http://%s/?id=AUTH123", a.listenAddr()))
				if err == nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cookie, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "COOKIEVALUE" {
		t.Fatalf("got cookie %q", cookie)
	}
}

func TestAuthenticateContextCancel(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()
	a := &Authenticator{
		GatewayURL:  gw.URL,
		ListenPort:  0,
		Client:      gw.Client(),
		OpenBrowser: func(string) error { return nil }, // browser never completes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestAuthenticateBadID(t *testing.T) {
	gw := fakeGateway(t)
	defer gw.Close()
	a := &Authenticator{GatewayURL: gw.URL, ListenPort: 0, Client: gw.Client()}
	a.OpenBrowser = func(string) error {
		go func() {
			time.Sleep(50 * time.Millisecond)
			http.Get(fmt.Sprintf("http://%s/?id=WRONG", a.listenAddr()))
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Authenticate(ctx); err == nil {
		t.Fatal("expected error for rejected auth id")
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/auth/`
Expected: FAIL (undefined `Authenticator`)

- [ ] **Step 3: Implement**

`internal/auth/auth.go`:

```go
// Package auth implements the Fortinet SAML external-browser login flow.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
)

type Authenticator struct {
	GatewayURL  string
	ListenPort  int
	Client      *http.Client
	OpenBrowser func(url string) error

	mu   sync.Mutex
	addr string // actual listen address once bound (host:port)
}

func (a *Authenticator) listenAddr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

// Authenticate runs the SAML flow and returns the SVPNCOOKIE value.
func (a *Authenticator) Authenticate(ctx context.Context) (string, error) {
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	openBrowser := a.OpenBrowser
	if openBrowser == nil {
		openBrowser = SystemOpenBrowser
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.ListenPort))
	if err != nil {
		return "", fmt.Errorf("saml listener: %w", err)
	}
	defer ln.Close()
	a.mu.Lock()
	a.addr = ln.Addr().String()
	a.mu.Unlock()

	type result struct {
		cookie string
		err    error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		cookie, err := a.exchange(ctx, client, id)
		if err != nil {
			http.Error(w, "login failed, check hyp-vpn logs", http.StatusBadGateway)
			done <- result{err: err}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Hyperio VPN connected — you can close this tab.</h2></body></html>")
		done <- result{cookie: cookie}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	loginURL := a.GatewayURL + "/remote/saml/start?redirect=1"
	if err := openBrowser(loginURL); err != nil {
		return "", fmt.Errorf("open browser: %w", err)
	}

	select {
	case r := <-done:
		return r.cookie, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("saml login not completed: %w", ctx.Err())
	}
}

// exchange trades the browser-delivered auth id for SVPNCOOKIE.
func (a *Authenticator) exchange(ctx context.Context, client *http.Client, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.GatewayURL+"/remote/saml/auth_id?id="+id, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway rejected auth id: %s", resp.Status)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "SVPNCOOKIE" && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", errors.New("no SVPNCOOKIE in gateway response")
}

// SystemOpenBrowser opens url in the OS default browser.
func SystemOpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/auth/`
Expected: PASS (all three tests)

- [ ] **Step 5: Commit**

```bash
git add internal/auth/
git commit -m "feat: SAML external-browser auth flow returning SVPNCOOKIE"
```

---

### Task 3: Tunnel supervisor

**Files:**
- Create: `internal/tunnel/tunnel.go`
- Test: `internal/tunnel/tunnel_test.go`

**Interfaces:**
- Consumes: an auth function matching `func(ctx context.Context) (string, error)` (Task 2's `Authenticator.Authenticate` bound as a method value).
- Produces:
  ```go
  package tunnel
  type State int
  const (
      Disconnected State = iota
      Authenticating
      Connecting
      Connected
      Reconnecting
      Error
  )
  func (s State) String() string
  type Event struct {
      State State
      Detail string // e.g. assigned IP when Connected, error text when Error
  }
  // ErrAuthRejected signals the backend refused the cookie (re-auth needed).
  var ErrAuthRejected = errors.New("tunnel: cookie rejected")
  type Supervisor struct { /* opaque */ }
  func New(authFn func(ctx context.Context) (string, error),
           runFn func(ctx context.Context, cookie string, connected func(ip string)) error,
           events chan<- Event) *Supervisor
  func (s *Supervisor) Connect()    // idempotent; starts loop if not running
  func (s *Supervisor) Disconnect() // stops loop and backend; no auto-restart
  // RunOpenconnect returns a runFn that spawns openconnect --protocol=fortinet.
  func RunOpenconnect(binPath, gatewayHostPort string, useSudo bool) func(ctx context.Context, cookie string, connected func(ip string)) error
  ```

**Supervisor loop semantics:** on `Connect()` → emit `Authenticating`, call authFn → emit `Connecting`, call runFn (blocks while tunnel alive; calls `connected(ip)` callback when backend reports up → emit `Connected`). runFn returning `ErrAuthRejected` → immediately re-auth (fresh cookie). Any other error/exit while not disconnected → emit `Reconnecting`, backoff 15s doubling to 2min cap, retry with same cookie first, re-auth on `ErrAuthRejected`. `Disconnect()` cancels the loop context → emit `Disconnected`. authFn error → emit `Error` with detail, loop stops (user must click Connect again).

- [ ] **Step 1: Write failing tests**

`internal/tunnel/tunnel_test.go`:

```go
package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// collect drains events into a slice via background goroutine.
func collect(ch <-chan Event, out *[]Event, stop chan struct{}) {
	go func() {
		for {
			select {
			case e := <-ch:
				*out = append(*out, e)
			case <-stop:
				return
			}
		}
	}()
}

func waitFor(t *testing.T, events *[]Event, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range *events {
			if e.State == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state %v never reached; got %+v", want, *events)
}

func TestConnectHappyPath(t *testing.T) {
	events := make(chan Event, 64)
	var seen []Event
	stop := make(chan struct{})
	defer close(stop)
	collect(events, &seen, stop)

	auth := func(ctx context.Context) (string, error) { return "COOKIE", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if cookie != "COOKIE" {
			t.Errorf("wrong cookie %q", cookie)
		}
		connected("10.212.134.5")
		<-ctx.Done() // stay "up" until disconnected
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	waitFor(t, &seen, Connected, 2*time.Second)
	s.Disconnect()
	waitFor(t, &seen, Disconnected, 2*time.Second)
}

func TestReconnectOnDrop(t *testing.T) {
	events := make(chan Event, 64)
	var seen []Event
	stop := make(chan struct{})
	defer close(stop)
	collect(events, &seen, stop)

	runs := make(chan struct{}, 8)
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		runs <- struct{}{}
		connected("10.212.134.5")
		return errors.New("link dropped") // simulated network drop
	}
	s := New(auth, run, events)
	s.backoffBase = 20 * time.Millisecond // test hook: shrink backoff
	s.Connect()
	// expect at least 2 runs (initial + restart after drop)
	for i := 0; i < 2; i++ {
		select {
		case <-runs:
		case <-time.After(3 * time.Second):
			t.Fatalf("run %d never happened", i+1)
		}
	}
	waitFor(t, &seen, Reconnecting, 2*time.Second)
	s.Disconnect()
}

func TestAuthRejectedTriggersReauth(t *testing.T) {
	events := make(chan Event, 64)
	var seen []Event
	stop := make(chan struct{})
	defer close(stop)
	collect(events, &seen, stop)

	authCalls := 0
	auth := func(ctx context.Context) (string, error) { authCalls++; return "C", nil }
	first := true
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if first {
			first = false
			return ErrAuthRejected
		}
		connected("10.212.134.5")
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.backoffBase = 20 * time.Millisecond
	s.Connect()
	waitFor(t, &seen, Connected, 3*time.Second)
	if authCalls < 2 {
		t.Fatalf("expected re-auth after ErrAuthRejected, auth calls = %d", authCalls)
	}
	s.Disconnect()
}

func TestAuthFailureStopsWithError(t *testing.T) {
	events := make(chan Event, 64)
	var seen []Event
	stop := make(chan struct{})
	defer close(stop)
	collect(events, &seen, stop)

	auth := func(ctx context.Context) (string, error) { return "", errors.New("saml timeout") }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		t.Fatal("run must not be called when auth fails")
		return nil
	}
	s := New(auth, run, events)
	s.Connect()
	waitFor(t, &seen, Error, 2*time.Second)
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/tunnel/`
Expected: FAIL (undefined symbols)

- [ ] **Step 3: Implement**

`internal/tunnel/tunnel.go`:

```go
// Package tunnel supervises the openconnect backend process.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type State int

const (
	Disconnected State = iota
	Authenticating
	Connecting
	Connected
	Reconnecting
	Error
)

func (s State) String() string {
	return [...]string{"Disconnected", "Authenticating", "Connecting", "Connected", "Reconnecting", "Error"}[s]
}

type Event struct {
	State  State
	Detail string
}

var ErrAuthRejected = errors.New("tunnel: cookie rejected")

type Supervisor struct {
	authFn func(ctx context.Context) (string, error)
	runFn  func(ctx context.Context, cookie string, connected func(ip string)) error
	events chan<- Event

	backoffBase time.Duration // exposed for tests
	backoffMax  time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
}

func New(authFn func(ctx context.Context) (string, error),
	runFn func(ctx context.Context, cookie string, connected func(ip string)) error,
	events chan<- Event) *Supervisor {
	return &Supervisor{
		authFn:      authFn,
		runFn:       runFn,
		events:      events,
		backoffBase: 15 * time.Second,
		backoffMax:  2 * time.Minute,
	}
}

func (s *Supervisor) emit(st State, detail string) {
	select {
	case s.events <- Event{State: st, Detail: detail}:
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
	s.mu.Unlock()
	go s.loop(ctx)
}

// Disconnect stops the loop and the backend. Idempotent.
func (s *Supervisor) Disconnect() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
}

func (s *Supervisor) loop(ctx context.Context) {
	defer s.emit(Disconnected, "")
	defer s.Disconnect() // clear cancel when loop exits on its own

	cookie := ""
	backoff := s.backoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		if cookie == "" {
			s.emit(Authenticating, "")
			c, err := s.authFn(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.emit(Error, "login failed: "+err.Error())
				}
				return
			}
			cookie = c
		}

		s.emit(Connecting, "")
		wasConnected := false
		err := s.runFn(ctx, cookie, func(ip string) {
			wasConnected = true
			backoff = s.backoffBase // healthy connection resets backoff
			s.emit(Connected, ip)
		})
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrAuthRejected) {
			cookie = "" // force re-auth, retry immediately
			continue
		}
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		s.emit(Reconnecting, detail)
		if !wasConnected {
			backoff = min(backoff*2, s.backoffMax)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

var connectedRe = regexp.MustCompile(`Connected as ([0-9a-fA-F.:]+)`)

// RunOpenconnect returns a runFn spawning openconnect --protocol=fortinet.
// useSudo prefixes "sudo -n" (macOS/Linux; Windows runs elevated already).
func RunOpenconnect(binPath, gatewayHostPort string, useSudo bool) func(ctx context.Context, cookie string, connected func(ip string)) error {
	return func(ctx context.Context, cookie string, connected func(ip string)) error {
		args := []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", gatewayHostPort}
		var cmd *exec.Cmd
		if useSudo {
			cmd = exec.CommandContext(ctx, "sudo", append([]string{"-n", binPath}, args...)...)
		} else {
			cmd = exec.CommandContext(ctx, binPath, args...)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = cmd.Stdout // openconnect logs to stderr; merge streams... see note below
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start openconnect: %w", err)
		}
		io.WriteString(stdin, cookie+"\n")
		stdin.Close()

		var lastLines []string
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			lastLines = append(lastLines, line)
			if len(lastLines) > 20 {
				lastLines = lastLines[1:]
			}
			if m := connectedRe.FindStringSubmatch(line); m != nil {
				connected(m[1])
			}
		}
		err = cmd.Wait()
		if err != nil && ctx.Err() == nil {
			tail := strings.Join(lastLines, "\n")
			if strings.Contains(tail, "Failed to obtain WebVPN cookie") ||
				strings.Contains(tail, "Unexpected 401") ||
				strings.Contains(tail, "cookie was rejected") {
				return ErrAuthRejected
			}
			return fmt.Errorf("openconnect exited: %w\n%s", err, tail)
		}
		return err
	}
}
```

Note for implementer: `cmd.Stderr = cmd.Stdout` where Stdout is a pipe does NOT merge automatically — instead assign both to the same pipe by using `cmd.StdoutPipe()` for stdout and `cmd.Stderr = cmd.Stdout` is invalid (Stdout is nil-backed). Correct approach: create one `io.Pipe` or set `cmd.Stdout = pw; cmd.Stderr = pw` with `pr` scanned. Implement it that way:

```go
pr, pw := io.Pipe()
cmd.Stdout = pw
cmd.Stderr = pw
// after cmd.Start(): go func() { cmd.Wait(); pw.Close() }() — then scan pr,
// and capture Wait's error from that goroutine via a channel.
```

(The test suite does not exercise `RunOpenconnect` — it is integration-tested manually in Task 6. Keep it small and obvious.)

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/tunnel/`
Expected: PASS (4 tests)

- [ ] **Step 5: Run full suite + vet**

Run: `go vet ./... && go test ./...`
Expected: clean

- [ ] **Step 6: Commit**

```bash
git add internal/tunnel/
git commit -m "feat: tunnel supervisor with backoff reconnect and openconnect runner"
```

---

### Task 4: Autostart package

**Files:**
- Create: `internal/autostart/autostart.go` (shared API + template rendering)
- Create: `internal/autostart/autostart_darwin.go`, `autostart_linux.go`, `autostart_windows.go`
- Test: `internal/autostart/autostart_test.go`

**Interfaces:**
- Consumes: nothing from other packages (takes the executable path as arg)
- Produces:
  ```go
  package autostart
  // Enable registers exePath as a login item. Disable removes it. IsEnabled reports it.
  func Enable(exePath string) error
  func Disable() error
  func IsEnabled() bool
  // exported for tests:
  func DarwinPlist(exePath string) string   // LaunchAgent XML
  func LinuxDesktop(exePath string) string  // XDG autostart .desktop content
  ```

**Per-OS behavior:**
- darwin: write `~/Library/LaunchAgents/com.hyperio.vpn.plist` (`RunAtLoad=true`, `Label=com.hyperio.vpn`, `ProgramArguments=[exePath]`, no KeepAlive), then `launchctl bootstrap gui/$UID <plist>` best-effort (ignore "already loaded" error). Disable: `launchctl bootout gui/$UID/com.hyperio.vpn` best-effort + remove file.
- linux: write `~/.config/autostart/hyp-vpn.desktop` with `Type=Application`, `Name=Hyperio VPN`, `Exec=<exePath>`, `X-GNOME-Autostart-enabled=true`. Disable: remove file.
- windows: `schtasks /Create /TN "HyperioVPN" /SC ONLOGON /RL HIGHEST /TR "<exePath>" /F`; Disable: `schtasks /Delete /TN "HyperioVPN" /F`; IsEnabled: `schtasks /Query /TN "HyperioVPN"` exit code.

- [ ] **Step 1: Write failing tests** (template rendering only — registration is OS-mutating, verified manually in Task 6)

`internal/autostart/autostart_test.go`:

```go
package autostart

import (
	"strings"
	"testing"
)

func TestDarwinPlist(t *testing.T) {
	p := DarwinPlist("/usr/local/bin/hyp-vpn")
	for _, want := range []string{
		"<key>Label</key>", "com.hyperio.vpn",
		"<key>RunAtLoad</key>", "<true/>",
		"/usr/local/bin/hyp-vpn",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "KeepAlive") {
		t.Error("plist must not contain KeepAlive (app supervises itself)")
	}
}

func TestLinuxDesktop(t *testing.T) {
	d := LinuxDesktop("/usr/local/bin/hyp-vpn")
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Name=Hyperio VPN",
		"Exec=/usr/local/bin/hyp-vpn",
	} {
		if !strings.Contains(d, want) {
			t.Errorf(".desktop missing %q:\n%s", want, d)
		}
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/autostart/`
Expected: FAIL

- [ ] **Step 3: Implement**

`internal/autostart/autostart.go`:

```go
// Package autostart registers the app as a per-user login item.
package autostart

import "fmt"

func DarwinPlist(exePath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.hyperio.vpn</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, exePath)
}

func LinuxDesktop(exePath string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Hyperio VPN
Exec=%s
X-GNOME-Autostart-enabled=true
`, exePath)
}
```

`internal/autostart/autostart_darwin.go`:

```go
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.hyperio.vpn.plist")
}

func Enable(exePath string) error {
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(DarwinPlist(exePath)), 0o644); err != nil {
		return err
	}
	// Best-effort load; "already bootstrapped" is fine.
	exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), p).Run()
	return nil
}

func Disable() error {
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/com.hyperio.vpn", os.Getuid())).Run()
	err := os.Remove(plistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func IsEnabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
```

`internal/autostart/autostart_linux.go`:

```go
package autostart

import (
	"os"
	"path/filepath"
)

func desktopPath() string {
	base, _ := os.UserConfigDir()
	return filepath.Join(base, "autostart", "hyp-vpn.desktop")
}

func Enable(exePath string) error {
	p := desktopPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(LinuxDesktop(exePath)), 0o644)
}

func Disable() error {
	err := os.Remove(desktopPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func IsEnabled() bool {
	_, err := os.Stat(desktopPath())
	return err == nil
}
```

`internal/autostart/autostart_windows.go`:

```go
package autostart

import "os/exec"

const taskName = "HyperioVPN"

func Enable(exePath string) error {
	return exec.Command("schtasks", "/Create", "/TN", taskName,
		"/SC", "ONLOGON", "/RL", "HIGHEST", "/TR", exePath, "/F").Run()
}

func Disable() error {
	return exec.Command("schtasks", "/Delete", "/TN", taskName, "/F").Run()
}

func IsEnabled() bool {
	return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}
```

- [ ] **Step 4: Run tests + cross-compile check**

Run: `go test ./internal/autostart/ && GOOS=linux GOARCH=amd64 go build ./... && GOOS=windows GOARCH=amd64 go build ./internal/autostart/`
Expected: tests PASS, both cross-builds succeed

- [ ] **Step 5: Commit**

```bash
git add internal/autostart/
git commit -m "feat: per-OS login-item autostart (launchd, XDG, schtasks)"
```

---

### Task 5: Tray UI + main wiring

**Files:**
- Create: `cmd/hyp-vpn/main.go`
- Create: `internal/tray/tray.go`, `internal/tray/icons.go`
- Modify: `go.mod` (add `fyne.io/systray`)

**Interfaces:**
- Consumes: `config.Load/Save/DefaultDir/GatewayURL`, `auth.Authenticator`, `tunnel.New/RunOpenconnect/Event/State`, `autostart.Enable/Disable/IsEnabled`
- Produces: the final binary. `tray.Run(app App)` where:
  ```go
  package tray
  type App interface {
      Connect()
      Disconnect()
      SetAutostart(on bool) error
      AutostartEnabled() bool
      LogPath() string
      Events() <-chan tunnel.Event
  }
  func Run(app App) // blocks; owns systray lifecycle
  ```

No unit tests for this task (systray needs a real GUI session); compile checks + manual smoke test instead. Keep ALL logic out of tray/main — they only wire and render.

- [ ] **Step 1: Add dependency**

```bash
go get fyne.io/systray@latest
```

- [ ] **Step 2: Implement icons**

`internal/tray/icons.go` — embedded monochrome PNGs (16/22px) as byte slices. Generate simple colored-dot icons with a tiny Go generator or use hand-made 1-color PNGs; commit the .png files under `internal/tray/assets/` and embed:

```go
package tray

import _ "embed"

//go:embed assets/icon_gray.png
var iconGray []byte // disconnected

//go:embed assets/icon_green.png
var iconGreen []byte // connected

//go:embed assets/icon_yellow.png
var iconYellow []byte // authenticating / connecting / reconnecting

//go:embed assets/icon_red.png
var iconRed []byte // error
```

Create the four PNGs (16×16 filled circle on transparent background) with any tool; ImageMagick one-liner works:

```bash
mkdir -p internal/tray/assets
for c in gray green yellow red; do
  magick -size 16x16 xc:none -fill "$c" -draw "circle 8,8 8,2" "internal/tray/assets/icon_$c.png"
done
```

(If `magick` is missing: `brew install imagemagick`.)

- [ ] **Step 3: Implement tray**

`internal/tray/tray.go`:

```go
// Package tray renders the systray menu; all logic lives in App.
package tray

import (
	"fmt"

	"fyne.io/systray"
	"github.com/hyperiosoftware/hyp-vpn/internal/tunnel"
	"github.com/hyperiosoftware/hyp-vpn/internal/xopen"
)

type App interface {
	Connect()
	Disconnect()
	SetAutostart(on bool) error
	AutostartEnabled() bool
	LogPath() string
	Events() <-chan tunnel.Event
}

func Run(app App) {
	systray.Run(func() { onReady(app) }, func() {})
}

func onReady(app App) {
	systray.SetIcon(iconGray)
	systray.SetTooltip("Hyperio VPN")

	status := systray.AddMenuItem("Disconnected", "")
	status.Disable()
	systray.AddSeparator()
	connect := systray.AddMenuItem("Connect", "")
	disconnect := systray.AddMenuItem("Disconnect", "")
	disconnect.Disable()
	systray.AddSeparator()
	auto := systray.AddMenuItemCheckbox("Auto-connect at login", "", app.AutostartEnabled())
	logs := systray.AddMenuItem("View logs", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	go func() {
		for {
			select {
			case e := <-app.Events():
				render(e, status, connect, disconnect)
			case <-connect.ClickedCh:
				app.Connect()
			case <-disconnect.ClickedCh:
				app.Disconnect()
			case <-auto.ClickedCh:
				if auto.Checked() {
					if app.SetAutostart(false) == nil {
						auto.Uncheck()
					}
				} else {
					if app.SetAutostart(true) == nil {
						auto.Check()
					}
				}
			case <-logs.ClickedCh:
				xopen.File(app.LogPath())
			case <-quit.ClickedCh:
				app.Disconnect()
				systray.Quit()
				return
			}
		}
	}()
}

func render(e tunnel.Event, status, connect, disconnect *systray.MenuItem) {
	switch e.State {
	case tunnel.Connected:
		systray.SetIcon(iconGreen)
		status.SetTitle(fmt.Sprintf("Connected — %s", e.Detail))
		connect.Disable()
		disconnect.Enable()
	case tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting:
		systray.SetIcon(iconYellow)
		status.SetTitle(e.State.String() + "…")
		connect.Disable()
		disconnect.Enable()
	case tunnel.Error:
		systray.SetIcon(iconRed)
		status.SetTitle("Error: " + e.Detail)
		connect.Enable()
		disconnect.Disable()
	default:
		systray.SetIcon(iconGray)
		status.SetTitle("Disconnected")
		connect.Enable()
		disconnect.Disable()
	}
}
```

Also create tiny helper package `internal/xopen/xopen.go` (opens a file/URL with the OS default handler — reused by auth's browser open if desired, but auth already has `SystemOpenBrowser`; keep them separate and simple):

```go
// Package xopen opens files with the OS default application.
package xopen

import (
	"os/exec"
	"runtime"
)

func File(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
```

- [ ] **Step 4: Implement main**

`cmd/hyp-vpn/main.go`:

```go
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

type app struct {
	cfg     *config.Config
	cfgDir  string
	sup     *tunnel.Supervisor
	events  chan tunnel.Event
	logPath string
}

func (a *app) Connect()                     { a.sup.Connect() }
func (a *app) Disconnect()                  { a.sup.Disconnect() }
func (a *app) AutostartEnabled() bool       { return autostart.IsEnabled() }
func (a *app) LogPath() string              { return a.logPath }
func (a *app) Events() <-chan tunnel.Event  { return a.events }

func (a *app) SetAutostart(on bool) error {
	var err error
	if on {
		exe, e := os.Executable()
		if e != nil {
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
	return a.cfg.Save(a.cfgDir)
}

func main() {
	cfgDir, err := config.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(cfgDir)
	if err != nil {
		log.Fatal(err)
	}
	os.MkdirAll(cfgDir, 0o700)
	logPath := filepath.Join(cfgDir, "hyp-vpn.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	authr := &auth.Authenticator{
		GatewayURL: cfg.GatewayURL(),
		ListenPort: cfg.SAMLPort,
		Client:     &http.Client{Timeout: 30 * time.Second},
	}
	authFn := func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return authr.Authenticate(ctx)
	}
	useSudo := runtime.GOOS != "windows"
	runFn := tunnel.RunOpenconnect(cfg.OpenconnectPath,
		fmt.Sprintf("%s:%d", cfg.Gateway, cfg.Port), useSudo)

	events := make(chan tunnel.Event, 16)
	a := &app{
		cfg: cfg, cfgDir: cfgDir,
		sup:     tunnel.New(authFn, loggedRun(runFn), events),
		events:  events,
		logPath: logPath,
	}

	if cfg.Autostart {
		a.sup.Connect() // connect on launch (launch happens at login)
	}
	tray.Run(a)
}

// loggedRun wraps runFn so every backend exit lands in the log file.
func loggedRun(run func(ctx context.Context, cookie string, connected func(string)) error) func(ctx context.Context, cookie string, connected func(string)) error {
	return func(ctx context.Context, cookie string, connected func(string)) error {
		log.Printf("tunnel: starting openconnect")
		err := run(ctx, cookie, func(ip string) {
			log.Printf("tunnel: connected as %s", ip)
			connected(ip)
		})
		log.Printf("tunnel: exited: %v", err)
		return err
	}
}
```

- [ ] **Step 5: Build + vet + test everything**

Run: `go vet ./... && go test ./... && go build ./cmd/hyp-vpn`
Expected: clean build, binary `hyp-vpn` in repo root (gitignored? add `/hyp-vpn` to .gitignore)

- [ ] **Step 6: Manual smoke test (macOS, no VPN yet)**

Run: `./hyp-vpn` — tray icon appears (gray), menu shows all items, Quit works. If openconnect not installed yet, Connect must end in red Error state with message in `~/Library/Application Support/hyp-vpn/hyp-vpn.log` — acceptable at this stage.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/tray/ internal/xopen/ go.mod go.sum .gitignore
git commit -m "feat: systray UI and main wiring"
```

---

### Task 6: Build tooling, install scripts, end-to-end verification

**Files:**
- Create: `Makefile`
- Create: `scripts/install.sh` (macOS + Linux), `scripts/install.ps1` (Windows)

**Interfaces:**
- Consumes: the `hyp-vpn` binary; openconnect from package manager
- Produces: `make release` → `dist/hyp-vpn-{darwin-arm64,darwin-amd64,linux-amd64,windows-amd64.exe}`

- [ ] **Step 1: Makefile**

```makefile
BIN := hyp-vpn
DIST := dist

.PHONY: build test release clean

build:
	go build -o $(BIN) ./cmd/hyp-vpn

test:
	go vet ./... && go test ./...

release: clean
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BIN)-darwin-arm64 ./cmd/hyp-vpn
	GOOS=darwin  GOARCH=amd64 go build -o $(DIST)/$(BIN)-darwin-amd64 ./cmd/hyp-vpn
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(DIST)/$(BIN)-linux-amd64 ./cmd/hyp-vpn
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o $(DIST)/$(BIN)-windows-amd64.exe ./cmd/hyp-vpn

clean:
	rm -rf $(DIST) $(BIN)
```

Note: darwin targets need cgo (systray uses Cocoa) — build them natively on the mac; `darwin-amd64` cross-build from arm64 mac works with `GOARCH=amd64 CGO_ENABLED=1`. If it fails, drop darwin-amd64 (team is on Apple Silicon).

- [ ] **Step 2: install.sh**

```bash
#!/usr/bin/env bash
# Installs hyp-vpn: openconnect dependency, binary, sudoers rule, autostart.
set -euo pipefail

REPO_URL="${HYP_VPN_RELEASE_URL:-}" # optional: URL of prebuilt binary
OS="$(uname -s)"

install_openconnect() {
  if command -v openconnect >/dev/null; then return; fi
  case "$OS" in
    Darwin) brew install openconnect ;;
    Linux)
      if command -v apt-get >/dev/null; then sudo apt-get install -y openconnect
      elif command -v dnf >/dev/null; then sudo dnf install -y openconnect
      elif command -v pacman >/dev/null; then sudo pacman -S --noconfirm openconnect
      else echo "install openconnect manually" >&2; exit 1; fi ;;
  esac
}

install_binary() {
  local target=/usr/local/bin/hyp-vpn
  if [[ -n "$REPO_URL" ]]; then
    curl -fsSL "$REPO_URL" -o /tmp/hyp-vpn && sudo install -m755 /tmp/hyp-vpn "$target"
  else
    # from a repo checkout
    make build
    sudo install -m755 hyp-vpn "$target"
  fi
}

install_sudoers() {
  local oc; oc="$(command -v openconnect)"
  local rule="$USER ALL=(root) NOPASSWD: $oc"
  echo "$rule" | sudo tee /etc/sudoers.d/hyp-vpn >/dev/null
  sudo chmod 440 /etc/sudoers.d/hyp-vpn
  sudo visudo -c >/dev/null || { sudo rm /etc/sudoers.d/hyp-vpn; echo "sudoers validation failed" >&2; exit 1; }
}

install_openconnect
install_binary
install_sudoers
/usr/local/bin/hyp-vpn &   # first launch registers autostart via default config
echo "hyp-vpn installed. Tray icon should be visible; click Connect."
```

- [ ] **Step 3: install.ps1**

```powershell
# Installs hyp-vpn on Windows: openconnect, binary, elevated logon task.
# Run from an elevated PowerShell.
$ErrorActionPreference = "Stop"
$dir = "$env:ProgramFiles\hyp-vpn"
New-Item -ItemType Directory -Force -Path $dir | Out-Null

# 1. openconnect (winget package provides openconnect + wintun)
if (-not (Get-Command openconnect -ErrorAction SilentlyContinue)) {
    winget install --accept-package-agreements --accept-source-agreements OpenConnect.OpenConnect
}

# 2. binary (expects hyp-vpn-windows-amd64.exe next to this script)
Copy-Item "$PSScriptRoot\hyp-vpn-windows-amd64.exe" "$dir\hyp-vpn.exe" -Force

# 3. elevated logon task (also serves as the elevation mechanism)
schtasks /Create /TN "HyperioVPN" /SC ONLOGON /RL HIGHEST /TR "$dir\hyp-vpn.exe" /F

# 4. start now
Start-ScheduledTask -TaskName "HyperioVPN"
Write-Host "hyp-vpn installed; tray icon should appear."
```

Implementer note: verify the winget package id with `winget search openconnect` at implementation time; if no suitable package exists, document manual download of an openconnect Windows build in README and set `openconnect_path` in config.json accordingly.

- [ ] **Step 4: Autostart flag wiring check**

The Windows scheduled task from install.ps1 and `autostart.Enable` use the same task name `HyperioVPN` — confirm both files use it verbatim.

- [ ] **Step 5: End-to-end test on this mac (the real acceptance test)**

```bash
bash scripts/install.sh
```

Then, with FortiClient quit:
1. Tray icon appears; Connect → browser opens SAML page → after login tray goes green with `Connected — 10.212.134.x`.
2. `ping <internal-host>` works (ask user for a known internal IP/host to verify).
3. Wi-Fi off/on → yellow `Reconnecting…` → green without interaction.
4. Disconnect from tray → gray, stays down.
5. Quit app, relaunch → auto-connects (config Autostart=true).
Expected: all five pass. Any failure: stop, debug with `~/Library/Application Support/hyp-vpn/hyp-vpn.log`, fix before proceeding.

- [ ] **Step 6: Commit**

```bash
git add Makefile scripts/
git commit -m "chore: build tooling and per-OS install scripts"
```

---

### Task 7: README

**Files:**
- Create: `README.md`

**Interfaces:** consumes everything; produces team-facing docs.

- [ ] **Step 1: Write README** covering, in this order:

1. What it is (one paragraph, mention it replaces FortiClient's missing auto-connect).
2. **Before installing:** quit FortiClient and disable its login item (System Settings → General → Login Items on macOS). Never run both clients at once.
3. Install per OS:
   - macOS/Linux: `bash scripts/install.sh` (from repo checkout, or `HYP_VPN_RELEASE_URL=<url> bash scripts/install.sh`)
   - Windows: elevated PowerShell, `.\scripts\install.ps1` with the release exe alongside.
   - macOS Gatekeeper note: unsigned binary → right-click → Open on first launch.
4. Usage: tray menu items; first connect opens a browser SAML login; later connects are silent until the IdP session expires.
5. Config file location and keys (`gateway`, `port`, `saml_port`, `openconnect_path`, `autostart`).
6. Troubleshooting: log file locations per OS; "openconnect not found"; sudoers rule check (`sudo -n openconnect --version` must not prompt); port 8020 already in use (edit `saml_port` — but note the FortiGate must allow the redirect port; 8020 is the supported default).
7. Uninstall per OS (remove binary, sudoers file, LaunchAgent/.desktop/schtask, config dir).

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: install, usage, troubleshooting"
```

---

## Self-Review (done during planning)

- **Spec coverage:** tray+status ✓ (T5), connect/disconnect ✓ (T3/T5), SAML ✓ (T2), autostart ✓ (T4), reconnect/backoff ✓ (T3), per-OS install + sudoers ✓ (T6), logs ✓ (T5/T7), FortiClient caveat ✓ (T7), release builds ✓ (T6).
- **Known risk flagged for implementer:** exact openconnect stderr strings for cookie rejection vary by version — `RunOpenconnect` matches three known variants; adjust after first real test in Task 6 Step 5 if reconnect-after-expiry misbehaves (symptom: endless yellow instead of browser popup).
- **Type consistency:** `tunnel.Event`/`State` names used in tray match Task 3 definitions; `App` interface in tray matches `app` struct methods in main; autostart task name `HyperioVPN` consistent between Go code and install.ps1.
