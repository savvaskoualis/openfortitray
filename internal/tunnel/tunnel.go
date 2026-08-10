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

// connectedRe matches openconnect's "Connected as <ip>" progress line.
var connectedRe = regexp.MustCompile(`Connected as ([0-9a-fA-F.:]+)`)

// authRejectedMarkers are openconnect log fragments meaning the cookie is no
// longer accepted by the gateway.
var authRejectedMarkers = []string{
	"Failed to obtain WebVPN cookie",
	"Unexpected 401",
	"cookie was rejected",
	"Cookie is no longer valid",
}

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
		// Interrupt rather than kill so openconnect tears the tunnel down and
		// restores routing; WaitDelay is the hard backstop.
		cmd.Cancel = func() error {
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = 10 * time.Second

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
		for _, marker := range authRejectedMarkers {
			if strings.Contains(tail, marker) {
				return ErrAuthRejected
			}
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
