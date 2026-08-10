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

	mu     sync.Mutex
	cancel context.CancelFunc
	gen    uint64 // identifies the running loop, so a stale loop cannot cancel a newer one
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
	s.gen++
	gen := s.gen
	s.mu.Unlock()
	go s.loop(ctx, gen)
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

func (s *Supervisor) loop(ctx context.Context, gen uint64) {
	defer s.emit(Disconnected, "")
	defer s.finish(gen)

	cookie := ""
	fresh := false // cookie was minted by the most recent authFn call
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
			cookie, fresh = c, true
		}

		s.emit(Connecting, "")
		// up may be set from another goroutine if runFn reports asynchronously.
		var up atomic.Bool
		err := s.runFn(ctx, cookie, func(ip string) {
			up.Store(true)
			if ctx.Err() == nil { // don't report "up" for a tunnel we just cancelled
				s.emit(Connected, ip)
			}
		})
		if ctx.Err() != nil {
			return
		}
		wasConnected := up.Load()
		if wasConnected {
			fresh = false // the cookie proved good; a later rejection means it went stale
			backoff = s.backoffBase
		}

		if errors.Is(err, ErrAuthRejected) {
			cookie = ""
			if !fresh {
				continue // stale cookie: re-authenticate immediately
			}
			// A brand-new cookie was refused; back off instead of spinning on
			// the SAML flow.
		}

		detail := ""
		if err != nil {
			detail = err.Error()
		}
		s.emit(Reconnecting, detail)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
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
		// Unblock the process's stdout/stderr writes if we stopped reading
		// early (e.g. an over-long line), so Wait cannot hang.
		pr.Close()

		err = <-waitErr
		if err != nil && ctx.Err() == nil {
			tail := strings.Join(lastLines, "\n")
			for _, marker := range authRejectedMarkers {
				if strings.Contains(tail, marker) {
					return ErrAuthRejected
				}
			}
			return fmt.Errorf("openconnect exited: %w\n%s", err, tail)
		}
		return err
	}
}
