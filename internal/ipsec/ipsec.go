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
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// RunFunc runs the IPsec backend until the tunnel goes down or ctx is
// cancelled. It calls connected(ip) once the backend reports the tunnel is
// up. connected MUST be called synchronously, on the same goroutine running
// this RunFunc — loop()'s bookkeeping around the callback is not
// synchronized against a call from any other goroutine. Implemented
// per-platform (strongswan_unix.go, ipsec_windows.go).
type RunFunc func(ctx context.Context, connected func(ip string)) error

// Supervisor keeps an IPsec tunnel up: runs the backend and reconnects with
// exponential backoff until told to stop.
type Supervisor struct {
	runFn  RunFunc
	events chan<- tunnel.Event

	backoffBase time.Duration // exposed for tests
	backoffMax  time.Duration
	minHealthy  time.Duration // time connected before a drop counts as "was healthy"
	prevWait    time.Duration // cap on waiting for the previous loop to tear down; exposed for tests

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
		prevWait:    20 * time.Second,
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
//
// If a previous loop is still tearing down (Disconnect cancels its context
// but does not wait for it to actually finish — e.g. swanctl --terminate and
// the connection-fragment file cleanup in NewStrongSwanRunFunc take a moment
// after cancellation), the new loop waits for it, bounded by prevWait, before
// touching the backend. Without this, a quick Disconnect immediately followed
// by Connect (onSystemWake's Disconnect()+Connect(), or a fast manual
// reconnect) could run two RunFunc invocations concurrently — the outgoing
// one's cleanup racing the incoming one's setup — over the SAME fixed
// connection name and swanctl conf.d files (see connName), corrupting
// whichever wrote last. Mirrors internal/tunnel.Supervisor's own prev-wait.
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

// finish clears the running loop's cancel func when the loop exits on its
// own (e.g. KeepAlive is off and a healthy session ends by itself, rather
// than via Disconnect). Without this, s.cancel stayed set after such an
// exit — Connect()'s "if s.cancel != nil { return }" idempotency guard then
// mistook the dead loop for a live one and refused to ever reconnect. A
// no-op if a newer loop has since been started. Mirrors
// internal/tunnel.Supervisor.finish exactly.
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

// friendlyDetail maps a permanent-failure error to a short message for the
// tray, mirroring internal/tunnel's own friendlyDetail: every ErrPermanent
// here is wrapped as "%w: <hint>..." (see the Windows PSK-refusal and
// strongSwan-not-found wrapping in ipsec_windows.go / strongswan_unix.go), so
// its Error() leads with "tunnel: install is broken: <hint>" — keep only that
// first line and drop the sentinel's own "tunnel: " prefix, leaving just the
// hint for the tray.
func friendlyDetail(err error) string {
	line := strings.SplitN(err.Error(), "\n", 2)[0]
	return strings.TrimPrefix(line, "tunnel: ")
}

func (s *Supervisor) loop(ctx context.Context, gen uint64, prev, done chan struct{}) {
	defer close(done) // runs last: the next loop waits for this
	emittedError := false
	defer func() {
		// Error is the terminal event; don't overwrite it with Disconnected.
		if !emittedError {
			s.emit(gen, tunnel.Disconnected, "")
		}
	}()
	defer s.finish(gen)

	// Never run two backends at once: wait for the previous loop (and its
	// swanctl/strongSwan state) to be fully gone before touching the tunnel.
	// Bounded — see Connect's doc comment and prevWait — because an unbounded
	// wait could leave the tray stuck in Connecting forever if the previous
	// loop's cleanup never finishes.
	if prev != nil {
		timeout := time.NewTimer(s.prevWait)
		defer timeout.Stop()
		select {
		case <-prev:
		case <-ctx.Done():
			return
		case <-timeout.C:
			emittedError = true
			s.emit(gen, tunnel.Error, "previous IPsec tunnel is still shutting down after "+
				s.prevWait.String()+"; restart the app (the VPN may still be up)")
			return
		}
	}

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
			return // deferred emit above reports Disconnected
		}

		// A permanent failure (e.g. a deterministic PSK refusal on Windows, or
		// swanctl missing on PATH) cannot be fixed by retrying, so retrying only
		// burns the user's time behind a "Reconnecting…" that will never
		// resolve. Report it and stop, exactly like internal/tunnel.Supervisor's
		// own ErrPermanent handling (see its loop's doc comment for why this is
		// gated on everConnected: a failure that looks permanent AFTER a
		// demonstrably working session is far more likely to be transient than
		// a real misconfiguration).
		if errors.Is(err, tunnel.ErrPermanent) && !everConnected {
			emittedError = true
			s.emit(gen, tunnel.Error, friendlyDetail(err))
			return
		}

		wasHealthy := !connectedAt.IsZero() && time.Since(connectedAt) >= s.minHealthy
		if everConnected && wasHealthy && !s.keepAliveEnabled() {
			return // deferred emit above reports Disconnected
		}

		detail := ""
		if err != nil {
			detail = err.Error()
			log.Printf("ipsec: connection attempt failed: %v", err)
		}
		s.emit(gen, tunnel.Reconnecting, detail)

		select {
		case <-ctx.Done():
			return // deferred emit above reports Disconnected
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > s.backoffMax {
			backoff = s.backoffMax
		}
	}
}
