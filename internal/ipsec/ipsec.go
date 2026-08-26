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
