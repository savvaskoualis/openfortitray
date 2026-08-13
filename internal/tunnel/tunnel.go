// Package tunnel supervises the openconnect backend process.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/dns"
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

// ErrPermanent marks a failure no amount of retrying can clear: the backend
// binary is not there, the privileged helper was never installed, or the sudoers
// rule that makes it callable without a password is gone. The supervisor turns it
// into the terminal Error state instead of spinning in Reconnecting forever,
// because the tray's "Reconnecting…" is a promise the loop cannot keep — the user
// has to fix the installation, and only a message that stays on screen tells them
// so.
var ErrPermanent = errors.New("tunnel: install is broken")

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
	prevWait    time.Duration // cap on waiting for the previous loop to tear down

	// earlyRetryDelay / maxEarlyRetries shape the quiet startup retry: a cookie
	// refused before the tunnel has ever come up this session is re-minted (a
	// fresh, non-interactive SAML login, tray stays on Connecting…) up to
	// maxEarlyRetries times, earlyRetryDelay apart, before the loud re-auth +
	// backoff path. See loop(). Exposed for tests.
	earlyRetryDelay time.Duration
	maxEarlyRetries int

	// maxConnectRounds bounds how many backoff rounds a Connect that has NEVER come
	// up may take before giving up with a terminal Error. Without a bound the loop
	// retried forever, and because each round re-minted the cookie, it opened a
	// browser tab per round — indefinitely. Exposed for tests.
	maxConnectRounds int
	// remintEveryRounds is how many refused backoff rounds pass before the cookie
	// is thrown away and SAML re-run. Re-minting is the only thing that opens a
	// browser, and when a gateway refuses because it still holds a previous session
	// it refuses FRESH cookies too (measured: five distinct cookies refused over
	// 3.5 minutes), so a new cookie every round buys nothing but tabs. Retrying the
	// cookie we have is silent and costs one ~0.3s attempt. Exposed for tests.
	remintEveryRounds int

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
		// Must exceed the runner's worst-case teardown (two helper stop attempts
		// then the WaitDelay backstop = 2*10s+12s = 32s), with margin, and no more:
		// past that point the previous backend is wedged, not slow.
		prevWait: 45 * time.Second,
		// A FortiGate that still holds a previous session refuses cookies until it
		// releases it. Measured over 25 reconnects on a real gateway, that took a
		// median of 25s and 90% of the time under ~75s (worst seen: 4.5 minutes). So
		// retry at a STEADY 5s for ~100s — same cookie, no browser, no backoff. A
		// backoff here is actively wrong: it makes us miss the moment the gateway
		// frees up and sit out a 30s or 60s wait for nothing.
		earlyRetryDelay: 5 * time.Second,
		maxEarlyRetries: 20,
		// ~8 rounds of a 15s..2min backoff spans several minutes, which covers the
		// worst observed wait for this gateway to release a session, and then stops
		// instead of retrying (and re-authenticating) forever.
		maxConnectRounds:  8,
		remintEveryRounds: 4,
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

// sessionEndedThreshold is how many auth-rejections AFTER a healthy session it
// takes to conclude the session was ended elsewhere (this gateway is
// one-session-per-user) rather than a benign expiry, and stop retrying so we do
// not pop the SAML browser unattended.
const sessionEndedThreshold = 3

// friendlyDetail maps a supervisor error to a SHORT, human message for the tray —
// never openconnect's multi-line route/stderr output or the helper's root-owner
// warning (those stay in the log). An empty string means the state label alone
// (e.g. "Reconnecting…") is enough.
func friendlyDetail(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuthRejected):
		return "" // transient; the Connecting/Reconnecting state says enough
	case errors.Is(err, ErrPermanent):
		// ErrPermanent leads with the install hint; keep only that first line and
		// drop the "tunnel: " prefix + the diagnostic tail.
		line := strings.SplitN(err.Error(), "\n", 2)[0]
		return strings.TrimPrefix(line, "tunnel: ")
	default:
		return "connection lost — reconnecting"
	}
}

func (s *Supervisor) loop(ctx context.Context, gen uint64, prev, done chan struct{}) {
	defer close(done) // runs last: the next loop waits for this
	emittedError := false
	defer func() {
		// Error is the terminal event; don't overwrite it with Disconnected.
		if !emittedError {
			s.emit(gen, Disconnected, "")
		}
	}()
	defer s.finish(gen)

	// Never run two backends at once: wait for the previous loop (and its
	// openconnect process) to be fully gone before touching the tunnel.
	//
	// Bounded, because that wait depends on a root process we may be unable to
	// signal: if the helper is unreachable and openconnect ignores its WaitDelay
	// kill, the previous loop never finishes and an unbounded wait would leave
	// the tray silently stuck in Connecting with no way out but a restart.
	// Starting a second openconnect anyway would have the two fight over the
	// routing table, so this Connect is abandoned with a message that says what
	// to do instead.
	if prev != nil {
		timeout := time.NewTimer(s.prevWait)
		defer timeout.Stop()
		select {
		case <-prev:
		case <-ctx.Done():
			return
		case <-timeout.C:
			emittedError = true
			s.emit(gen, Error, "previous tunnel is still shutting down after "+
				s.prevWait.String()+"; restart the app (the VPN may still be up)")
			return
		}
	}

	cookie := ""
	proven := false        // this cookie carried a healthy connection at some point
	everConnected := false // the tunnel came up at least once since this Connect
	immediateReauths := 0
	earlyRetries := 0 // quiet re-mints used before the first bring-up
	// postHealthyRejects counts auth-rejections AFTER the tunnel has been healthy
	// this Connect (any healthy bring-up clears the tally; a non-auth failure in
	// between does not). This gateway allows one SSL-VPN session per user,
	// so when another device logs in it kills ours and our cookie starts getting
	// rejected. A one-off is a benign expiry; a run of them means the session was
	// taken elsewhere — surface that calmly and back off hard instead of fighting.
	postHealthyRejects := 0
	// rejectRounds counts refused rounds since the last mint (gates the periodic
	// re-mint); connectRounds counts backoff rounds taken without ever coming up
	// (gates the terminal give-up).
	rejectRounds := 0
	connectRounds := 0
	// connectingDetail explains a connect that is taking a while, because a bare
	// "Connecting…" sitting there for a minute reads as stuck.
	//
	// It deliberately reports the SYMPTOM rather than naming a cause. An earlier
	// version claimed "the VPN allows one session per user", which was a guess that
	// turned out to be wrong most of the time — the usual cause was a truncated
	// cookie (see the helper's cmd_start) — and the app then confidently told users
	// something false about their gateway. If the reason cannot be established from
	// here, say only what is known.
	connectingDetail := ""

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
					emittedError = true
					s.emit(gen, Error, "sign-in didn't complete — click Connect")
				}
				return
			}
			cookie, proven = c, false
		}

		s.emit(gen, Connecting, connectingDetail)
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
			everConnected = true
			backoff = s.backoffBase
			postHealthyRejects = 0 // a fresh healthy bring-up clears the session-taken tally
			if time.Since(time.Unix(0, connectedAt.Load())) >= s.minHealthy {
				proven = true // the cookie worked for a real session
			}
		}

		// A permanent failure means the installation is broken, so retrying only
		// burns the user's time behind a "Reconnecting…" that will never resolve.
		// Report it and stop; Error is terminal, which also leaves Connect
		// clickable so a fixed install can be tried without restarting the app.
		//
		// Gated on everConnected — the whole session, not just this attempt —
		// because a tunnel that demonstrably worked a moment ago was installed
		// correctly. A permanent-looking failure after that is far more likely to
		// be transient (sudo momentarily unavailable, the package manager
		// mid-upgrade, config management rewriting /etc/sudoers.d) than a real
		// misconfiguration, and killing a working session's supervisor over it
		// would be worse than backing off and trying again.
		if errors.Is(err, ErrPermanent) && !everConnected {
			emittedError = true
			s.emit(gen, Error, friendlyDetail(err))
			return
		}

		if errors.Is(err, ErrAuthRejected) {
			// A rejected cookie is DEAD: re-mint, do not re-send it.
			//
			// 0.1.21 re-sent the same cookie several times first, on the theory that
			// this gateway refuses cookies while it reaps a previous session. Measuring
			// 43 real attempts falsified that: a freshly minted cookie was accepted on
			// the first try in 3 of 3 cases, while a reused/stored one was refused in
			// all 30 — the gateway's SVPNCOOKIE is bound to its session, so a cookie it
			// has rejected once cannot start working. Re-sending only delayed the SAML
			// that actually recovers, by maxSameCookieRetries * sameCookieDelay (~12s
			// of every slow connect the user reported).
			//
			// The re-mint is quiet and non-interactive right after an interactive
			// login: the IdP browser session is still cached, so it completes in ~1s
			// with no clicks. The loop top re-emits Authenticating/Connecting (never
			// Reconnecting), so the tray stays calm instead of flashing
			// "Reconnecting — cookie rejected". Bounded by maxEarlyRetries, so a
			// gateway that refuses every fresh cookie cannot spin the browser flow.
			// Once the tunnel has come up even once, everConnected is true and a later
			// rejection takes the proven / immediate-reauth / backoff paths unchanged.
			if !everConnected && earlyRetries < s.maxEarlyRetries {
				earlyRetries++
				connectingDetail = "gateway refused the session — retrying"
				// KEEP the cookie. These early retries used to re-mint, which meant a
				// SAML login — and a browser tab — every 4 seconds: three tabs before a
				// connect succeeded, which is what users saw. Re-minting cannot help
				// here anyway: when the gateway refuses because it still holds a previous
				// session it refuses freshly minted cookies just as readily (measured:
				// five distinct cookies refused across 3.5 minutes). Only time helps, so
				// retry the cookie we have, silently. A cookie that really is dead is
				// covered by the periodic re-mint on the backoff rounds below.
				select {
				case <-time.After(s.earlyRetryDelay):
				case <-ctx.Done():
					return
				}
				continue
			}
			if !everConnected {
				// Never came up yet: KEEP the cookie between backoff rounds unless a
				// re-mint is due. A gateway refusing because it still holds a previous
				// session refuses fresh cookies just as readily, so minting one per round
				// only opens a browser tab per round — what users saw as "tabs keep
				// opening". Retrying the cookie we have is silent and costs one ~0.3s
				// attempt; the periodic re-mint still covers a cookie that really expired.
				rejectRounds++
				if rejectRounds%s.remintEveryRounds == 0 {
					cookie = ""
				}
			} else {
				// After a healthy session a rejection means the session was killed
				// server-side, so the cookie is genuinely dead and a fresh one is the only
				// way back: re-mint at once, as before.
				cookie = ""
				postHealthyRejects++
				if postHealthyRejects >= sessionEndedThreshold {
					// A run of rejections after a healthy session: this one-per-user
					// gateway almost certainly handed our slot to another login. Re-
					// running SAML now would pop the browser and, unattended (e.g.
					// overnight), just time out. Stop and leave Connect clickable so the
					// user signs in again when they choose, instead of spinning.
					emittedError = true
					s.emit(gen, Error, "VPN session ended — click Connect to sign in")
					return
				}
			}
			if proven && immediateReauths < maxImmediateReauths {
				// A cookie that once carried a healthy session has gone stale
				// (e.g. server-side session kill): re-authenticate at once.
				immediateReauths++
				continue
			}
			// Otherwise back off before minting another cookie, so a gateway
			// that refuses fresh cookies cannot spin the SAML browser flow.
		}

		// A Connect that has never come up cannot retry forever: bound it, then say
		// so and stop. Unbounded retrying kept the tray in "Reconnecting…" and, when
		// each round re-minted, opened a browser tab every round for as long as the
		// app was running. Once the tunnel HAS come up, this does not apply — a real
		// session deserves indefinite reconnection attempts.
		if !everConnected {
			connectRounds++
			if connectRounds >= s.maxConnectRounds {
				emittedError = true
				s.emit(gen, Error, "couldn't connect — click Connect to try again")
				return
			}
		}
		s.emit(gen, Reconnecting, friendlyDetail(err))
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
//	Configured as 10.0.0.5, with SSL connected and DTLS connected
//
// (verified against the format string in openconnect v9.21:
// "Configured as %s%s%s, with SSL%s%s %s and %s%s%s %s"). openconnect 7.x used
// "Connected as <ip>", so both spellings are accepted. Note the deliberate
// " as ": the earlier "Connected to <gw-ip>:<port>" line carries the *gateway*
// address and must not be mistaken for ours.
var connectedRe = regexp.MustCompile(`(?:Configured|Connected) as ([0-9a-fA-F.:]+)`)

// maxHandshakeLogLines caps how many openconnect progress lines are mirrored into
// the log per attempt (see the scan loop in RunOpenconnect). A handshake is a
// dozen lines; the cap only matters for a gateway that never finishes one.
const maxHandshakeLogLines = 60

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

// permanentMarkers are output fragments meaning the privileged path itself is
// not set up, as opposed to the tunnel failing. Matched case-insensitively, so
// they must be written in lower case (TestPermanentMarkersAreLowercase enforces
// that).
//
// Both come from our own side of the boundary, not from openconnect, which is
// what makes them safe to treat as terminal:
//
//   - "a password is required" is sudo's reply to `sudo -n` when the
//     /etc/sudoers.d/openfortitray rule is missing, does not name this user, or names
//     a different helper path. No cookie, gateway or network state can produce
//     it, and no retry can clear it.
//   - "not installed: run scripts/install.sh" is scripts/openfortitray-tunnel's own
//     guard, printed when the helper still carries the @OPENCONNECT@ placeholder
//     because it was copied into place without going through the installer.
//
// Deliberately narrow. A marker that also fires on ordinary trouble would strand
// the user in a terminal Error over a hiccup, which is the opposite failure and
// the harder one to recover from (only Connect gets them out of it).
var permanentMarkers = []string{
	"a password is required",
	"not installed: run scripts/install.sh",
}

// isPermanent reports whether the backend's output says the install is broken
// rather than the connection.
func isPermanent(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range permanentMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// installHint leads the detail of every permanent failure — before the
// diagnostics, not after them. Every one of them is fixed the same way, and the
// tray keeps only the first line of a detail and clips it at 60 runes, so an
// instruction placed after the process output would be the part that gets cut.
// The diagnostics go on the following lines, where the log file still has them.
//
// The wording is OS-aware. On unix the permanent failures are the privileged
// path being unset — a missing/mismatched sudoers rule ("a password is
// required"), or the helper still carrying its @OPENCONNECT@ placeholder ("not
// installed: run scripts/install.sh") — all repaired by re-running
// scripts/install.sh, which does not exist on Windows. On Windows there is no
// helper: the app runs openconnect directly, so the only permanent failure is
// openconnect itself not being found (the exec.ErrNotFound branch below), and
// scripts/install.sh guidance would be a dead end. There the fix is to reinstall
// via the installer and let the app run elevated.
var installHint = installHintFor(runtime.GOOS)

// installHintFor returns the one-line remediation hint for goos. Split out from
// the package var so a test can exercise both platforms regardless of the host.
// The detail's first line is "tunnel: install is broken: " (27 runes, from
// ErrPermanent) followed by this hint, and the tray clips that whole line at 60
// runes — so a hint must stay at or under ~33 runes to survive the clip.
func installHintFor(goos string) string {
	if goos == "windows" {
		return "reinstall as Administrator"
	}
	return "re-run scripts/install.sh"
}

// DefaultHelperPath is where scripts/install.sh puts the privileged helper.
const DefaultHelperPath = "/usr/local/libexec/openfortitray-tunnel"

// helperPIDFile mirrors PIDFILE in scripts/openfortitray-tunnel. Nothing in this
// package reads it — the helper owns it, because only root may write there — but
// the tests assert the helper does not create it for a rejected gateway.
const helperPIDFile = "/var/run/openfortitray-openconnect.pid"

const (
	// helperStopTimeout bounds one privileged teardown call. The helper waits up
	// to STOP_TIMEOUT (8s) for openconnect to exit cleanly after SIGINT — long
	// enough for openconnect to send its logout request to the FortiGate so no
	// session lingers server-side — so allow a little more here than that window.
	helperStopTimeout = 10 * time.Second
	// helperWaitDelay is the backstop *after* teardown has been attempted, and it
	// needs no headroom for the retry: os/exec's watchCtx calls Cancel to
	// completion and only then creates the WaitDelay timer, so the two are
	// consecutive rather than concurrent. Worst case for a cancelled run is
	// therefore helperStopAttempts*helperStopTimeout + helperWaitDelay, which is
	// what cmd/openfortitray's shutdownWait has to cover — keep them in step.
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
	// HelperPath is the root-owned helper script (scripts/openfortitray-tunnel) run
	// through sudo on macOS/Linux. Empty means DefaultHelperPath.
	HelperPath string
	// UseSudo runs the tunnel as root via `sudo -n <HelperPath>`; false runs
	// openconnect directly (Windows, where the app is already elevated).
	UseSudo bool

	// The following mirror the active profile's tunnel-shaping toggles. They are
	// appended to the openconnect argv on BOTH paths (see openconnectFlags /
	// startArgv). On the direct path they follow the fixed flags directly; on the
	// privileged path they are passed to the helper's "start" subcommand after the
	// gateway (`start <gateway> <flags...>`). This does NOT reopen the
	// arbitrary-option hole: the helper validates every flag against an exact
	// allowlist (only --no-dtls, --disable-ipv6 and --servercert <fingerprint>,
	// none of which takes a script/command openconnect could run as root) and
	// rejects anything else before it reaches openconnect. openconnectFlags only
	// ever emits members of that allowlist, so the two sides stay in lockstep.

	// DTLS mirrors profile.DTLS. openconnect uses DTLS/ESP by default, so only a
	// false value emits a flag: --no-dtls.
	DTLS bool
	// DualStack mirrors profile.DualStack. openconnect requests IPv6 by default
	// when the gateway offers it, so dual-stack ON needs no flag; dual-stack OFF
	// emits --disable-ipv6 to force IPv4-only.
	DualStack bool
	// ServerCertMode mirrors profile.ServerCert.Mode ("warn"/"trust"/"pin").
	ServerCertMode string
	// ServerCertPin is the fingerprint passed to --servercert when pinning.
	ServerCertPin string

	// ConfDir is the directory the DIRECT path writes its short-lived openconnect
	// config file into (the one carrying the cookie). Empty falls back to a
	// subdirectory of the system temp dir. Unused on the privileged path.
	ConfDir string

	// Logout, when non-nil, is called with the cookie a session used, once the
	// backend for that session has exited. It exists because openconnect has no
	// logout for the Fortinet protocol at all — its logout endpoints cover Juniper
	// (dana-na/auth/logout.cgi) and GlobalProtect (ssl-vpn/logout.esp) only — so
	// closing the tunnel leaves the session ESTABLISHED on the gateway until the
	// gateway's own timeout. On a one-SSL-VPN-session-per-user gateway that means
	// every reconnect is refused in the meantime: measured, five separate freshly
	// minted cookies were rejected over 3.5 minutes after a clean disconnect before
	// one was accepted. FortiClient does not have this problem because it sends the
	// logout, which is what this hook is for.
	//
	// It is called only when the session actually came up (no session, nothing to
	// log out), from the runFn's goroutine before it returns — so a caller that
	// waits for teardown, as the app's shutdown path does, waits for this too. It
	// must be bounded and best-effort: a gateway that will not answer must not be
	// able to hold up an exit.
	Logout func(cookie string)

	// SplitDNS lists the domains whose lookups must go to the VPN-pushed DNS via
	// macOS per-domain scoped resolvers (Profile.SplitDNS). When non-empty on the
	// privileged (sudo helper) path, the discovered DNS is installed with the
	// helper's "dns-set" once the tunnel is up and removed with "dns-clear" on
	// teardown — this is what makes corp names resolve while a global override
	// (Tailscale MagicDNS) owns the primary resolver. Empty disables it.
	// cmd/openfortitray only populates this on macOS; Linux scoped DNS is not
	// automated yet (TODO(linux-splitdns) in internal/dns and main.go).
	SplitDNS []string

	// sudoPath overrides the sudo binary; tests use it to substitute a stub.
	sudoPath string
	// reapRunner overrides command execution in ReapStale; tests substitute a
	// recorder. nil means run the command for real (execReap).
	reapRunner func(ctx context.Context, name string, args []string) error
	// discoverDNS discovers the VPN-pushed DNS server once the tunnel is up. nil
	// uses the platform default (dns.Discover). Tests inject a stub.
	discoverDNS func(ctx context.Context, hintDomains []string) (string, error)
	// dnsRunner runs the helper's dns-set/dns-clear (sudo -n helper ...). nil runs
	// the command for real. Tests substitute a recorder.
	dnsRunner func(ctx context.Context, name string, args []string) error
}

// Server-certificate modes, mirrored from config.ServerCertMode as plain strings
// so this package stays free of a config import.
const (
	certModeWarn  = "warn"
	certModeTrust = "trust"
	certModePin   = "pin"
)

// openconnectFlags derives the extra openconnect command-line flags from the
// profile toggles. The same set is used on both the direct and the privileged
// path (see startArgv). Every flag it can emit is on the helper's allowlist, so
// the privileged path validates rather than rejects them.
//
// Flag choices (verified against openconnect 9.x / GnuTLS):
//   - DTLS false  → --no-dtls          (openconnect enables DTLS/ESP by default)
//   - DualStack false → --disable-ipv6 (openconnect asks for IPv6 by default;
//     there is no positive "dual-stack" flag — enabling it is the default)
//   - ServerCert pin  → --servercert <pin> (accept only that fingerprint)
//   - ServerCert trust: modern openconnect has NO "accept any invalid cert"
//     option (the old --no-cert-check was removed for security). The only
//     documented accept mechanism is --servercert with a specific fingerprint,
//     so trust falls back to that IF a pin is also present; with no pin it emits
//     NOTHING (a documented no-op at the openconnect layer — the connection will
//     still reject an unknown cert). We deliberately do not disable validation.
//   - ServerCert warn (default): no flag (system trust; invalid certs fail).
func (o Options) openconnectFlags() []string {
	var flags []string
	if !o.DTLS {
		flags = append(flags, "--no-dtls")
	}
	if !o.DualStack {
		flags = append(flags, "--disable-ipv6")
	}
	switch o.ServerCertMode {
	case certModePin:
		if o.ServerCertPin != "" {
			flags = append(flags, "--servercert", o.ServerCertPin)
		}
	case certModeTrust:
		// No blanket accept-invalid flag exists; honour an explicit pin if given.
		if o.ServerCertPin != "" {
			flags = append(flags, "--servercert", o.ServerCertPin)
		}
	}
	return flags
}

// confDir is where the direct path writes its short-lived openconnect config. It
// sits beside the app's own state (the caller sets ConfDir; os.TempDir is the
// fallback) rather than in a world-writable temp root.
func (o Options) confDir() string {
	if o.ConfDir != "" {
		return o.ConfDir
	}
	return filepath.Join(os.TempDir(), "openfortitray")
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
//
// The tunnel-shaping flags follow the gateway on the privileged path
// (`start <gateway> <flags...>`) and follow the fixed flags on the direct path.
// They are the same set; the helper validates each against an exact allowlist,
// so threading them through sudo does not widen what can run as root.
// startArgv returns the command to run. confPath is the openconnect config file
// holding the cookie on the DIRECT path (Windows); it is empty on the privileged
// path, where the helper writes its own config from the cookie it reads on stdin.
func (o Options) startArgv(confPath string) (string, []string) {
	if o.UseSudo {
		args := []string{"-n", o.helperPath(), "start", o.Gateway}
		args = append(args, o.openconnectFlags()...)
		return o.sudo(), args
	}
	// --config, not --cookie-on-stdin: openconnect reads a stdin cookie into a
	// 1024-byte buffer and silently truncates anything longer, and this gateway's
	// cookies routinely exceed that (1288 bytes observed), which surfaces only as
	// an opaque "Cookie was rejected by server". Not --cookie either: that would put
	// the session cookie in the process table for the tunnel's whole life.
	args := []string{"--protocol=fortinet", "--config", confPath, "--non-inter"}
	args = append(args, o.openconnectFlags()...)
	args = append(args, o.Gateway)
	return o.OpenconnectPath, args
}

// cookieConfigFile writes an openconnect config file carrying the cookie, for the
// direct (Windows) path, and returns its path. The caller removes it once
// openconnect has exited.
//
// SECURITY: an openconnect config file accepts ANY option, including script=,
// which openconnect executes — and on Windows the app is elevated, so that would
// be an administrator-level command. validateCookie is what stops a cookie from
// introducing a second line; it runs before anything is written.
func cookieConfigFile(dir, cookie string) (string, error) {
	if err := validateCookie(cookie); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "openconnect-*.conf")
	if err != nil {
		return "", fmt.Errorf("cannot create the openconnect config: %w", err)
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("cannot restrict %s: %w", f.Name(), err)
	}
	if _, err := fmt.Fprintf(f, "cookie=SVPNCOOKIE=%s\n", cookie); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("cannot write the openconnect config: %w", err)
	}
	return f.Name(), nil
}

// cookieRe is the character set a FortiGate SVPNCOOKIE uses (base64/hex with
// URL-safe and percent-encoding punctuation). Anything else — whitespace, a
// newline, a control character — could start a new line in the config file, so it
// is refused rather than escaped. Mirrors validate_cookie in the privileged
// helper; the two must stay in step.
var cookieRe = regexp.MustCompile(`^[A-Za-z0-9._~%=+/-]+$`)

func validateCookie(cookie string) error {
	if cookie == "" {
		return errors.New("empty cookie")
	}
	if strings.HasPrefix(cookie, "-") {
		return errors.New("cookie must not start with '-'")
	}
	if !cookieRe.MatchString(cookie) {
		return errors.New("cookie contains characters that are not valid in a session cookie")
	}
	return nil
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

// ReapStale asks the privileged helper to tear down any tunnel a previous,
// unclean exit left running. A hard crash — SIGKILL of the app, a panic, power
// loss — skips the in-process teardown entirely, orphaning a root openconnect
// that keeps the FortiGate session alive; and because the gateway allows one
// session per user, every reconnect then fails "Cookie was rejected" in a loop
// until the orphan is killed by hand. Calling this on startup, before a new
// cookie is minted, clears that orphan and its gateway session so even a hard
// crash self-heals on the next launch.
//
// It is best-effort and bounded by ctx: the helper's "stop" is idempotent and
// exits 0 when there is nothing to reap, so the common (clean) startup returns
// fast. On the direct path (Windows, where the app is elevated and there is no
// privileged helper) there is nothing to reap through a helper, so it is a
// no-op. Any error is returned for the caller to log, not to act on — startup
// proceeds regardless.
func (o Options) ReapStale(ctx context.Context) error {
	name, args, viaHelper := o.stopArgv()
	if !viaHelper {
		return nil
	}
	run := o.reapRunner
	if run == nil {
		run = execReap
	}
	return run(ctx, name, args)
}

// execReap is the real command runner behind ReapStale.
func execReap(ctx context.Context, name string, args []string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// helperStopAttempts is how many times teardown is asked for before giving up.
//
// Two, because the cost of not tearing the tunnel down is a machine left on the
// VPN with a root openconnect the app cannot signal. Note what the retry does and
// does not buy: the helper's "stop" is idempotent and exits 0 when there is no pid
// to signal, so a second attempt cannot help with a missing or stale pidfile. It
// covers the transient failures only — sudo itself failing, or the first attempt
// exceeding helperStopTimeout while openconnect was still winding down.
const helperStopAttempts = 2

// runHelperStop asks the privileged helper to interrupt openconnect and waits
// for it to finish. It runs on its own timeout because the caller's context is
// already cancelled by the time Cancel is invoked.
//
// If the helper cannot be reached at all (not installed, sudoers rule missing)
// there is no way to signal the root process, so we kill our sudo child to avoid
// hanging Wait and return a loud error: the tunnel may still be up, and that has
// to reach the log.
func runHelperStop(cmd *exec.Cmd, name string, args []string) error {
	var err error
	var out []byte
	for attempt := 1; attempt <= helperStopAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
		out, err = exec.CommandContext(ctx, name, args...).CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
	}
	killErr := cmd.Process.Kill()
	return fmt.Errorf("tunnel teardown via %s failed after %d attempts: %w: %s "+
		"(killed sudo: %v); the tunnel may still be up",
		name, helperStopAttempts, err, strings.TrimSpace(string(out)), killErr)
}

// Split-DNS timing. Kept as vars so tests can shrink the discovery wait.
var (
	// dnsDiscoverAttempts / dnsDiscoverInterval bound the wait for openconnect's
	// vpnc-script to install the pushed DNS after the "Configured as" line: the
	// two happen close together but not simultaneously, so discovery is retried.
	dnsDiscoverAttempts = 10
	dnsDiscoverInterval = 500 * time.Millisecond
)

const (
	// dnsSetTimeout / dnsClearTimeout bound a single privileged dns-set/dns-clear
	// helper call. dns-clear runs on teardown, where the run's own context is
	// already cancelled, so it uses a fresh bounded context of its own.
	dnsSetTimeout   = 10 * time.Second
	dnsClearTimeout = 8 * time.Second
)

// splitDNSEnabled reports whether scoped-resolver handling should run: only on
// the privileged (sudo helper) path — the unprivileged app must never write
// /etc/resolver itself — and only when the profile actually lists domains.
func (o Options) splitDNSEnabled() bool { return o.UseSudo && len(o.SplitDNS) > 0 }

// dnsSetArgv is the privileged command that installs the scoped resolvers:
// `sudo -n <helper> dns-set <dns-ip> <domain>...`. The helper validates the IP
// and every domain before touching /etc/resolver, so threading them through sudo
// widens nothing (the sudoers rule matches the helper path, not its argv).
func (o Options) dnsSetArgv(dnsIP string) (string, []string) {
	args := append([]string{"-n", o.helperPath(), "dns-set", dnsIP}, o.SplitDNS...)
	return o.sudo(), args
}

// dnsClearArgv is the privileged command that removes OUR scoped resolvers:
// `sudo -n <helper> dns-clear <domain>...`. The helper only removes files it
// stamped, so a pre-existing /etc/resolver entry survives.
func (o Options) dnsClearArgv() (string, []string) {
	args := append([]string{"-n", o.helperPath(), "dns-clear"}, o.SplitDNS...)
	return o.sudo(), args
}

// runDNS executes a dns-set/dns-clear command, capturing output for the log.
func (o Options) runDNS(ctx context.Context, name string, args []string) error {
	if o.dnsRunner != nil {
		return o.dnsRunner(ctx, name, args)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installSplitDNS discovers the VPN-pushed DNS server (retrying while
// vpnc-script catches up) and points the profile's split-DNS domains at it via
// the helper's dns-set. Best-effort: every failure is logged and swallowed —
// split-DNS is a coexistence convenience, not something whose failure should
// take down a working tunnel. Returns when ctx is cancelled (teardown).
func (o Options) installSplitDNS(ctx context.Context) {
	discover := o.discoverDNS
	if discover == nil {
		discover = dns.Discover
	}
	var dnsIP string
	for attempt := 0; attempt < dnsDiscoverAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if ip, err := discover(ctx, o.SplitDNS); err == nil && ip != "" {
			dnsIP = ip
			break
		}
		select {
		case <-time.After(dnsDiscoverInterval):
		case <-ctx.Done():
			return
		}
	}
	if dnsIP == "" {
		log.Printf("tunnel: split-DNS: could not discover the VPN DNS server; %v left unscoped", o.SplitDNS)
		return
	}
	if ctx.Err() != nil {
		return // disconnected while discovering; don't install what clear won't be told about
	}
	name, args := o.dnsSetArgv(dnsIP)
	// A fresh context, not the run's: a set that has been decided on should
	// complete atomically. Teardown is handled by clearSplitDNS afterwards.
	cctx, cancel := context.WithTimeout(context.Background(), dnsSetTimeout)
	defer cancel()
	if err := o.runDNS(cctx, name, args); err != nil {
		log.Printf("tunnel: split-DNS: dns-set failed (best-effort): %v", err)
		return
	}
	log.Printf("tunnel: split-DNS: scoped %v to %s", o.SplitDNS, dnsIP)
}

// clearSplitDNS removes the scoped resolvers via the helper's dns-clear. It runs
// on teardown with a fresh bounded context, since the run's context is already
// cancelled by then. Best-effort and idempotent (dns-clear exits 0 with nothing
// to remove).
func (o Options) clearSplitDNS() {
	name, args := o.dnsClearArgv()
	ctx, cancel := context.WithTimeout(context.Background(), dnsClearTimeout)
	defer cancel()
	if err := o.runDNS(ctx, name, args); err != nil {
		log.Printf("tunnel: split-DNS: dns-clear failed (best-effort): %v", err)
	}
}

// withSplitDNS wraps a runFn so scoped resolvers are installed once the tunnel
// reports up and removed when the run ends. When split-DNS is off it returns the
// runFn unchanged, so the core path is untouched. The install runs on its own
// goroutine (off the output-scan loop), and the wrapper waits for it before
// clearing so a set can never race past the clear and orphan a resolver file.
func (o Options) withSplitDNS(inner func(ctx context.Context, cookie string, connected func(ip string)) error) func(ctx context.Context, cookie string, connected func(ip string)) error {
	if !o.splitDNSEnabled() {
		return inner
	}
	return func(ctx context.Context, cookie string, connected func(ip string)) error {
		var once sync.Once
		var wg sync.WaitGroup
		wrapped := func(ip string) {
			connected(ip) // report the tunnel up first; DNS is a follow-on
			once.Do(func() {
				wg.Add(1)
				go func() {
					defer wg.Done()
					o.installSplitDNS(ctx)
				}()
			})
		}
		err := inner(ctx, cookie, wrapped)
		wg.Wait() // let an in-flight install finish before we remove
		o.clearSplitDNS()
		return err
	}
}

// RunOpenconnect returns a runFn that runs openconnect --protocol=fortinet,
// directly or through the privileged helper (see Options). On the privileged
// path with a non-empty SplitDNS it also installs and removes macOS scoped
// resolvers around the connection (see withSplitDNS).
func RunOpenconnect(opts Options) func(ctx context.Context, cookie string, connected func(ip string)) error {
	base := func(ctx context.Context, cookie string, connected func(ip string)) error {
		// On the direct path the cookie travels in a config file we write here (see
		// startArgv for why not stdin and not the argv). On the privileged path it
		// still goes to the helper on stdin and the helper writes its own file, so
		// nothing is written here and confPath stays empty.
		confPath := ""
		if !opts.UseSudo {
			p, err := cookieConfigFile(opts.confDir(), cookie)
			if err != nil {
				return fmt.Errorf("openconnect config: %w", err)
			}
			confPath = p
			// Removed once openconnect has exited: it re-reads nothing after startup,
			// but leaving a session cookie on disk for the life of the tunnel — or
			// after a crash — is not acceptable.
			defer os.Remove(confPath)
		}
		name, args := opts.startArgv(confPath)
		cmd := exec.CommandContext(ctx, name, args...)
		if stopName, stopArgs, viaHelper := opts.stopArgv(); viaHelper {
			cmd.Cancel = func() error { return runHelperStop(cmd, stopName, stopArgs) }
			cmd.WaitDelay = helperWaitDelay
		} else {
			// The direct path is Windows in production (cmd/openfortitray sets
			// UseSudo = GOOS != "windows"), and on Windows this branch always
			// kills: os.Process.Signal(os.Interrupt) is unimplemented there and
			// returns an error unconditionally, so the fallback below is the only
			// path actually taken. A hard kill means openconnect never runs its
			// own teardown, so routes and the wintun adapter are left as they
			// were — reconnecting or rebooting is what restores them. This is a
			// known limitation, not a bug to work around here: interrupting a
			// Windows process requires a console-control-event dance
			// (GenerateConsoleCtrlEvent against a shared console group) that
			// would have to be built into the child's startup, and the app is
			// elevated on that platform so the routing damage is repairable.
			//
			// The Signal attempt is kept for the POSIX case, which the tests
			// exercise (TestRunOpenconnectDirectPathIsSignalled) and which is
			// reachable in production only if UseSudo is ever set false on
			// macOS/Linux: there SIGINT makes openconnect tear the tunnel down
			// and restore routing itself. WaitDelay is the hard backstop for
			// both.
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
		// openconnect splits its output: progress (including the "Configured as
		// <ip>" line we parse) goes to stdout at default verbosity, while errors
		// (including the cookie-rejection markers) go to stderr. Both matter, so
		// merge them into one pipe and scan that.
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		if err := cmd.Start(); err != nil {
			stdin.Close()
			pw.Close()
			pr.Close()
			// Nothing to execute: either sudo is missing (privileged path) or
			// openconnect is not there (direct path). Every retry would look the
			// same, so mark it terminal and say what fixes it.
			//
			// Two error shapes, because os/exec produces different ones: a bare
			// name that PATH lookup cannot resolve yields exec.ErrNotFound, while
			// a path with separators is used verbatim and fails at execve with
			// ENOENT. The second is the common one on Windows, where
			// openconnect_path is an absolute install path — classifying only the
			// first would leave that platform looping forever.
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%w: %s\ncannot run %s: %w", ErrPermanent, installHint, name, err)
			}
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

		// sessionUp records that the gateway actually established a session for this
		// cookie, which is the precondition for logging it out below. Written and read
		// only on this goroutine (the scan loop, then after Wait).
		sessionUp := false
		var lastLines []string
		sc := bufio.NewScanner(pr)
		// Handshake timing. openconnect's own progress lines are the only visibility
		// into where a connect spends its time, but logging the whole stream is not
		// an option: once the tunnel is up the same stream carries per-route
		// teardown chatter that floods the log. So mirror the lines only until the
		// tunnel comes up — exactly the window we want to measure — each prefixed
		// with the elapsed time since exec, and bounded in case a gateway is chatty.
		// After that, the ring buffer above still captures the tail for errors.
		started := time.Now()
		handshakeLines := 0
		for sc.Scan() {
			line := sc.Text()
			lastLines = append(lastLines, line)
			if len(lastLines) > 20 {
				lastLines = lastLines[1:]
			}
			if m := connectedRe.FindStringSubmatch(line); m != nil {
				log.Printf("openconnect: [%6.2fs] tunnel up as %s", time.Since(started).Seconds(), m[1])
				handshakeLines = maxHandshakeLogLines // stop mirroring: the rest is traffic
				sessionUp = true
				connected(m[1])
				continue
			}
			if handshakeLines < maxHandshakeLogLines {
				handshakeLines++
				log.Printf("openconnect: [%6.2fs] %s", time.Since(started).Seconds(), line)
			}
		}
		scanErr := sc.Err()
		// Unblock the process's stdout/stderr writes if we stopped reading
		// early (e.g. an over-long line), so Wait cannot hang.
		pr.Close()

		err = <-waitErr
		// End the session on the GATEWAY, not just locally. openconnect has no
		// Fortinet logout, so without this the session stays established server-side
		// until the gateway times it out, and a one-session-per-user gateway refuses
		// every reconnect until then (see Options.Logout). Done before the returns
		// below so it happens on every exit path — a user disconnect, a drop, or a
		// shutdown — whenever a session existed.
		if opts.Logout != nil && sessionUp {
			opts.Logout(cookie)
		}
		if ctx.Err() != nil {
			return err // we asked it to stop; the exit reason is irrelevant
		}
		tail := strings.Join(lastLines, "\n")
		// Check for rejection regardless of exit status: openconnect can report
		// a refused cookie and still exit 0.
		if isAuthRejected(tail) {
			// Wrap (not replace) the sentinel so errors.Is still matches while the
			// log carries openconnect's literal words — the difference between a
			// genuinely dead cookie and the gateway refusing the session.
			return fmt.Errorf("%w: %s", ErrAuthRejected, strings.TrimSpace(tail))
		}
		// Likewise regardless of exit status, and checked before the generic exit
		// error below so the wrapping carries the sentinel. Whether this actually
		// ends the session is the supervisor's call: it retries a permanent-looking
		// failure that follows a healthy connection (see loop()).
		if isPermanent(tail) {
			return fmt.Errorf("%w: %s\n%s", ErrPermanent, installHint, strings.TrimSpace(tail))
		}
		if err != nil {
			return fmt.Errorf("openconnect exited: %w\n%s", err, tail)
		}
		if scanErr != nil {
			return fmt.Errorf("reading openconnect output: %w\n%s", scanErr, tail)
		}
		return nil
	}
	return opts.withSplitDNS(base)
}
