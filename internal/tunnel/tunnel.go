// Package tunnel supervises the openconnect backend process.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// State is the tunnel lifecycle state reported to the UI.
type State int

const (
	Disconnected State = iota
	Authenticating
	Connecting
	Connected
	Reconnecting
	Error
)

var stateNames = [...]string{
	"Disconnected",
	"Authenticating",
	"Connecting",
	"Connected",
	"Reconnecting",
	"Error",
}

func (s State) String() string {
	if s < 0 || int(s) >= len(stateNames) {
		return fmt.Sprintf("State(%d)", int(s))
	}
	return stateNames[s]
}

// Event is a state transition. Detail carries the assigned IP when Connected
// and the error text when Reconnecting or Error.
type Event struct {
	State  State
	Detail string
}

// ErrAuthRejected signals the backend refused the cookie (re-auth needed).
var ErrAuthRejected = errors.New("tunnel: cookie rejected")

// AuthFunc obtains a fresh VPN cookie.
type AuthFunc func(ctx context.Context) (string, error)

// RunFunc runs the backend until the tunnel goes down or ctx is cancelled. It
// calls connected(ip) once the backend reports the tunnel is up.
type RunFunc func(ctx context.Context, cookie string, connected func(ip string)) error

// Supervisor keeps the tunnel up: authenticates, runs the backend and
// reconnects with exponential backoff until told to stop.
type Supervisor struct {
	authFn AuthFunc
	runFn  RunFunc
	events chan<- Event

	backoffBase time.Duration // exposed for tests
	backoffMax  time.Duration
	minHealthy  time.Duration // time connected before a cookie counts as proven

	mu     sync.Mutex
	cancel context.CancelFunc
	gen    uint64        // identifies the running loop: stale loops cannot cancel or emit
	done   chan struct{} // closed when the current loop has fully torn down
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
		minHealthy:  30 * time.Second,
	}
}

// emit delivers an event unless a newer loop generation has taken over, so a
// loop that is still winding down cannot report state for the live tunnel.
func (s *Supervisor) emit(gen uint64, st State, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return
	}
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
	s.gen++
	gen := s.gen
	prev := s.done // previous loop, possibly still tearing down
	done := make(chan struct{})
	s.done = done
	s.mu.Unlock()
	go s.loop(ctx, gen, prev, done)
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

// Wait blocks until the supervision loop has fully torn down — including the
// backend process, whose interrupt-and-exit is what restores routing — or until
// ctx is done. It returns immediately when no loop has ever started or the last
// one already finished. Call it after Disconnect to shut down cleanly.
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

// finish clears the running loop's cancel func when the loop exits on its own.
// It is a no-op if a newer loop has since been started.
func (s *Supervisor) finish(gen uint64) {
	s.mu.Lock()
	cancel := s.cancel
	if s.gen == gen {
		s.cancel = nil
	} else {
		cancel = nil // a newer loop owns s.cancel; leave it alone
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// maxImmediateReauths caps consecutive zero-delay re-authentications, so a
// gateway that keeps rejecting cookies cannot spin the SAML flow.
const maxImmediateReauths = 1

func (s *Supervisor) loop(ctx context.Context, gen uint64, prev, done chan struct{}) {
	defer close(done) // runs last: the next loop waits for this
	authFailed := false
	defer func() {
		// Error is the terminal event when login failed; don't overwrite it.
		if !authFailed {
			s.emit(gen, Disconnected, "")
		}
	}()
	defer s.finish(gen)

	// Never run two backends at once: wait for the previous loop (and its
	// openconnect process) to be fully gone before touching the tunnel.
	if prev != nil {
		select {
		case <-prev:
		case <-ctx.Done():
			return
		}
	}

	cookie := ""
	proven := false // this cookie carried a healthy connection at some point
	immediateReauths := 0
	backoff := s.backoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		if cookie == "" {
			s.emit(gen, Authenticating, "")
			c, err := s.authFn(ctx)
			if err != nil {
				if ctx.Err() == nil {
					authFailed = true
					s.emit(gen, Error, "login failed: "+err.Error())
				}
				return
			}
			cookie, proven = c, false
		}

		s.emit(gen, Connecting, "")
		// These may be written from another goroutine if runFn reports
		// asynchronously, hence the atomics.
		var up atomic.Bool
		var connectedAt atomic.Int64 // unix nanos of the first "up" report
		err := s.runFn(ctx, cookie, func(ip string) {
			up.Store(true)
			connectedAt.CompareAndSwap(0, time.Now().UnixNano())
			if ctx.Err() == nil { // don't report "up" for a tunnel we just cancelled
				s.emit(gen, Connected, ip)
			}
		})
		if ctx.Err() != nil {
			return
		}
		wasConnected := up.Load()
		if wasConnected {
			backoff = s.backoffBase
			if time.Since(time.Unix(0, connectedAt.Load())) >= s.minHealthy {
				proven = true // the cookie worked for a real session
			}
		}

		if errors.Is(err, ErrAuthRejected) {
			cookie = ""
			if proven && immediateReauths < maxImmediateReauths {
				// A cookie that once carried a healthy session has gone stale
				// (e.g. server-side session kill): re-authenticate at once.
				immediateReauths++
				continue
			}
			// Otherwise back off before minting another cookie, so a gateway
			// that refuses fresh cookies cannot spin the SAML browser flow.
		}

		detail := ""
		if err != nil {
			detail = err.Error()
		}
		s.emit(gen, Reconnecting, detail)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		immediateReauths = 0 // a delay was paid; allow one fast re-auth again
		backoff *= 2
		if backoff > s.backoffMax {
			backoff = s.backoffMax
		}
	}
}

// connectedRe matches the openconnect progress line that reports the address
// assigned to the tunnel. openconnect 8.x/9.x print
//
//	Configured as 10.212.134.5, with SSL connected and DTLS connected
//
// (verified against the format string in openconnect v9.21:
// "Configured as %s%s%s, with SSL%s%s %s and %s%s%s %s"). openconnect 7.x used
// "Connected as <ip>", so both spellings are accepted. Note the deliberate
// " as ": the earlier "Connected to <gw-ip>:<port>" line carries the *gateway*
// address and must not be mistaken for ours.
var connectedRe = regexp.MustCompile(`(?:Configured|Connected) as ([0-9a-fA-F.:]+)`)

// authRejectedMarkers are openconnect log fragments meaning the cookie is no
// longer accepted by the gateway, so a fresh SAML login is required. They are
// matched case-insensitively and must therefore be written in lower case
// (TestAuthRejectedMarkersAreLowercase enforces that).
//
// Wording verified against openconnect v9.21. Deliberately conservative: a
// marker that also fires on plain network trouble would open a browser window
// for a re-login every time Wi-Fi hiccups, so generic connection failures
// ("Error establishing Fortinet connection") are left out — those are retried
// with the existing cookie instead.
//
// The FortiGate-specific one is the important one in practice. On FortiOS 5+ a
// dead SVPNCOOKIE does not produce any of the generic "cookie" wordings: the
// config fetch in fortinet_get_config() returns -EPERM (the gateway answered
// 401/403) and openconnect prints "Fortinet server is rejecting request for
// connection options" instead. Without that marker a stale cookie looks like
// plain link trouble and the supervisor retries it forever with the same dead
// cookie, leaving the tray stuck on "Reconnecting…" until the user restarts the
// app. openconnect's own text hedges that this is also "observed after
// reconnection in some cases" — re-authenticating is the right recovery there
// too, and the supervisor's backoff for unproven cookies stops a gateway that
// refuses fresh cookies from spinning the SAML browser flow.
var authRejectedMarkers = []string{
	"cookie was rejected by server",     // mainloop: gateway refused the cookie
	"cookie is no longer valid",         // session invalidated server-side
	"session terminated by server",      // admin/idle kill: cookie is dead
	"failed to complete authentication", // auth handshake refused
	"failed to obtain webvpn cookie",    // legacy openconnect 7.x wording
	// fortinet.c: config request answered 401/403, i.e. the cookie is dead.
	"rejecting request for connection options",
	// fortinet.c: the cookie we supplied is not a usable SVPNCOOKIE at all.
	"no cookie named svpncookie",
}

// isAuthRejected reports whether openconnect's output says the cookie was
// refused rather than the link merely failing.
func isAuthRejected(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range authRejectedMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// DefaultHelperPath is where scripts/install.sh puts the privileged helper.
const DefaultHelperPath = "/usr/local/libexec/hyp-vpn-tunnel"

const (
	// helperStopTimeout bounds the privileged teardown call. The helper waits up
	// to 6s for openconnect to exit cleanly, so allow a little more.
	helperStopTimeout = 10 * time.Second
	// helperWaitDelay must exceed helperStopTimeout: killing our sudo child is a
	// last resort that leaves the root openconnect (and its routes) behind, so
	// the helper gets its full chance first.
	helperWaitDelay = 12 * time.Second
	// signalWaitDelay backstops the direct path, where we can signal the
	// process ourselves.
	signalWaitDelay = 10 * time.Second
)

// Options configures the openconnect runner.
type Options struct {
	// Gateway is the FortiGate SSL-VPN endpoint as "host:port".
	Gateway string
	// OpenconnectPath is the openconnect binary, used only when the app is
	// already privileged (Windows) and runs it directly.
	OpenconnectPath string
	// HelperPath is the root-owned helper script (scripts/hyp-vpn-tunnel) run
	// through sudo on macOS/Linux. Empty means DefaultHelperPath.
	HelperPath string
	// UseSudo runs the tunnel as root via `sudo -n <HelperPath>`; false runs
	// openconnect directly (Windows, where the app is already elevated).
	UseSudo bool

	// sudoPath overrides the sudo binary; tests use it to substitute a stub.
	sudoPath string
}

func (o Options) helperPath() string {
	if o.HelperPath == "" {
		return DefaultHelperPath
	}
	return o.HelperPath
}

func (o Options) sudo() string {
	if o.sudoPath == "" {
		return "sudo"
	}
	return o.sudoPath
}

// startArgv returns the command that brings the tunnel up. Privileged runs go
// through the helper's "start" subcommand rather than openconnect directly, so
// the NOPASSWD sudoers rule can be scoped to one script with validated
// arguments instead of to openconnect (whose --script/--csd-wrapper options
// would amount to passwordless root).
func (o Options) startArgv() (string, []string) {
	if o.UseSudo {
		return o.sudo(), []string{"-n", o.helperPath(), "start", o.Gateway}
	}
	return o.OpenconnectPath, []string{
		"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", o.Gateway,
	}
}

// stopArgv returns the command that tears the tunnel down, and whether one is
// needed. It is: an unprivileged parent cannot signal a root openconnect —
// kill(2) fails with EPERM — so teardown has to be asked for through the
// helper, which runs as root. Killing our own child (sudo) instead would leave
// the tunnel and its routes in place. On the direct path we signal the process
// ourselves and no stop command is needed.
func (o Options) stopArgv() (string, []string, bool) {
	if !o.UseSudo {
		return "", nil, false
	}
	return o.sudo(), []string{"-n", o.helperPath(), "stop"}, true
}

// runHelperStop asks the privileged helper to interrupt openconnect and waits
// for it to finish. It runs on its own timeout because the caller's context is
// already cancelled by the time Cancel is invoked.
//
// If the helper cannot be reached (not installed, sudoers rule missing) there is
// no way to signal the root process, so we kill our sudo child to avoid hanging
// Wait and return a loud error: the tunnel may still be up, and that has to
// reach the log.
func runHelperStop(cmd *exec.Cmd, name string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	killErr := cmd.Process.Kill()
	return fmt.Errorf("tunnel teardown via %s failed: %w: %s (killed sudo: %v); "+
		"the tunnel may still be up", name, err, strings.TrimSpace(string(out)), killErr)
}

// RunOpenconnect returns a runFn that runs openconnect --protocol=fortinet,
// directly or through the privileged helper (see Options).
func RunOpenconnect(opts Options) func(ctx context.Context, cookie string, connected func(ip string)) error {
	return func(ctx context.Context, cookie string, connected func(ip string)) error {
		name, args := opts.startArgv()
		cmd := exec.CommandContext(ctx, name, args...)
		if stopName, stopArgs, viaHelper := opts.stopArgv(); viaHelper {
			cmd.Cancel = func() error { return runHelperStop(cmd, stopName, stopArgs) }
			cmd.WaitDelay = helperWaitDelay
		} else {
			// Interrupt rather than kill so openconnect tears the tunnel down
			// and restores routing; WaitDelay is the hard backstop.
			cmd.Cancel = func() error {
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					return cmd.Process.Kill()
				}
				return nil
			}
			cmd.WaitDelay = signalWaitDelay
		}

		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		// openconnect logs progress to stderr, so merge both streams into one
		// pipe and scan that.
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		if err := cmd.Start(); err != nil {
			stdin.Close()
			pw.Close()
			pr.Close()
			return fmt.Errorf("start openconnect: %w", err)
		}

		waitErr := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			pw.Close() // EOF for the scanner below
			waitErr <- err
		}()

		// A write failure here means the process is already gone; Wait reports why.
		_, _ = io.WriteString(stdin, cookie+"\n")
		stdin.Close()

		var lastLines []string
		sc := bufio.NewScanner(pr)
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
		scanErr := sc.Err()
		// Unblock the process's stdout/stderr writes if we stopped reading
		// early (e.g. an over-long line), so Wait cannot hang.
		pr.Close()

		err = <-waitErr
		if ctx.Err() != nil {
			return err // we asked it to stop; the exit reason is irrelevant
		}
		tail := strings.Join(lastLines, "\n")
		// Check for rejection regardless of exit status: openconnect can report
		// a refused cookie and still exit 0.
		if isAuthRejected(tail) {
			return ErrAuthRejected
		}
		if err != nil {
			return fmt.Errorf("openconnect exited: %w\n%s", err, tail)
		}
		if scanErr != nil {
			return fmt.Errorf("reading openconnect output: %w\n%s", scanErr, tail)
		}
		return nil
	}
}
