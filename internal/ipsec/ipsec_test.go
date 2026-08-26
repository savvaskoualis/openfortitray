package ipsec

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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

	// loop.emit(Connecting) always fires before runFn is invoked, so
	// Connecting is enqueued ahead of started being signalled above; drain
	// past it (as the other tests in this file do) rather than assuming
	// Connected is the first event on the channel.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.State == tunnel.Connecting {
				continue
			}
			if ev.State != tunnel.Connected || ev.Detail != "10.0.0.5" {
				t.Errorf("got %+v, want Connected/10.0.0.5", ev)
			}
			return
		case <-deadline:
			t.Fatal("no Connected event")
		}
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
	var attempts atomic.Int32
	run := func(ctx context.Context, connected func(ip string)) error {
		attempts.Add(1)
		return errors.New("swanctl: no response from charon")
	}
	s := New(run, events)
	s.backoffBase = 10 * time.Millisecond
	s.backoffMax = 20 * time.Millisecond
	s.Connect()
	defer s.Disconnect()

	deadline := time.After(2 * time.Second)
	for attempts.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d attempt(s) after 2s, want at least 2", attempts.Load())
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

// A permanent failure (e.g. the Windows PSK refusal, or swanctl missing) must
// emit a terminal Error and stop — never Reconnecting — when it happens
// before the tunnel has ever come up this Connect. Mirrors
// internal/tunnel.Supervisor's own ErrPermanent handling (Important #2).
func TestErrPermanentEmitsTerminalErrorInsteadOfReconnecting(t *testing.T) {
	events := make(chan tunnel.Event, 8)
	var attempts atomic.Int32
	run := func(ctx context.Context, connected func(ip string)) error {
		attempts.Add(1)
		return fmt.Errorf("%w: swanctl not found on PATH — install strongSwan", tunnel.ErrPermanent)
	}
	s := New(run, events)
	s.backoffBase = 10 * time.Millisecond
	s.Connect()
	defer s.Disconnect()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.State == tunnel.Connecting {
				continue
			}
			if ev.State != tunnel.Error {
				t.Fatalf("got state %v, want a terminal Error (never Reconnecting) for a permanent failure", ev.State)
			}
			if ev.Detail == "" {
				t.Error("the terminal Error must carry the install-hint detail")
			}
			goto checkNoRetry
		case <-deadline:
			t.Fatal("no event received")
		}
	}
checkNoRetry:
	// Give a buggy implementation a chance to retry anyway before asserting it
	// didn't.
	time.Sleep(100 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Errorf("runFn called %d times, want exactly 1 (a permanent failure must not retry)", got)
	}
}

// A permanent-LOOKING failure after the tunnel has already been up once this
// Connect must still retry — the same everConnected gate
// internal/tunnel.Supervisor uses, because a demonstrably working session is
// far more likely to have hit a transient blip than a real misconfiguration.
func TestErrPermanentAfterEverConnectedStillRetries(t *testing.T) {
	events := make(chan tunnel.Event, 8)
	var attempts atomic.Int32
	run := func(ctx context.Context, connected func(ip string)) error {
		n := attempts.Add(1)
		if n == 1 {
			connected("10.0.0.5")
			return fmt.Errorf("%w: swanctl not found on PATH", tunnel.ErrPermanent)
		}
		return fmt.Errorf("%w: swanctl not found on PATH", tunnel.ErrPermanent)
	}
	s := New(run, events)
	s.backoffBase = 5 * time.Millisecond
	s.backoffMax = 10 * time.Millisecond
	s.Connect()
	defer s.Disconnect()

	deadline := time.After(2 * time.Second)
	for attempts.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d attempt(s) after 2s, want at least 2 (a permanent-looking failure after a healthy connect must retry)", attempts.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Critical #1's fix makes a.ipsecSup one long-lived object, reused across
// Connect/Disconnect rather than reconstructed via ipsec.New() on every
// startTunnel call. But onSystemWake still does Disconnect() immediately
// followed by Connect() on that SAME object, and Connect() must not start a
// new RunFunc invocation before the previous one has actually finished
// tearing down (swanctl --terminate, removing the connection-fragment
// files) — otherwise the old loop's cleanup races the new loop's setup over
// the SAME fixed connection name/files (see connName), which is exactly
// what Important #6 described, just one level down: a single reused object
// still needs this guard, the same way internal/tunnel.Supervisor's Connect
// already waits for a previous loop via its own prevWait.
func TestConnectWaitsForPreviousLoopToTearDownBeforeReconnecting(t *testing.T) {
	events := make(chan tunnel.Event, 32)
	var inFlight atomic.Int32
	var sawConcurrent atomic.Bool
	run := func(ctx context.Context, connected func(ip string)) error {
		if inFlight.Add(1) > 1 {
			sawConcurrent.Store(true)
		}
		defer inFlight.Add(-1)
		connected("10.0.0.5")
		<-ctx.Done()
		// Simulate real teardown work (swanctl --terminate, file removal)
		// taking a moment after cancellation, exactly like
		// NewStrongSwanRunFunc's ctx.Done() branch.
		time.Sleep(150 * time.Millisecond)
		return ctx.Err()
	}
	s := New(run, events)
	s.prevWait = 2 * time.Second
	s.Connect()
	time.Sleep(50 * time.Millisecond) // let it reach Connected

	// Mirrors onSystemWake: Disconnect immediately followed by Connect, with
	// no external synchronization on the previous loop's teardown.
	s.Disconnect()
	s.Connect()
	defer s.Disconnect()

	time.Sleep(400 * time.Millisecond)
	if sawConcurrent.Load() {
		t.Error("RunFunc ran concurrently across a Disconnect+Connect — Connect must wait for the previous loop's teardown before starting a new one")
	}
}

// A loop that exits on its own (KeepAlive off, a healthy session ending by
// itself rather than via Disconnect) must clear s.cancel so a later Connect
// can actually reconnect — without this, Connect's "already running"
// idempotency guard mistook the dead loop for a live one and silently
// refused to reconnect forever. Mirrors internal/tunnel.Supervisor.finish.
func TestConnectReconnectsAfterKeepAliveOffEndedSessionOnItsOwn(t *testing.T) {
	events := make(chan tunnel.Event, 32)
	var calls atomic.Int32
	run := func(ctx context.Context, connected func(ip string)) error {
		calls.Add(1)
		connected("10.0.0.5")
		return nil // the tunnel ends on its own; no external Disconnect
	}
	s := New(run, events)
	s.minHealthy = 0
	s.SetKeepAlive(false)
	s.Connect()
	defer s.Disconnect()
	time.Sleep(100 * time.Millisecond)

	s.Connect() // a later reconnect (e.g. the user clicks Connect again)
	time.Sleep(100 * time.Millisecond)

	if got := calls.Load(); got != 2 {
		t.Errorf("runFn called %d times, want 2 (Connect after a self-ended session must actually reconnect, not silently no-op)", got)
	}
}

func TestSetKeepAliveFalseStopsRetryingAfterHealthySession(t *testing.T) {
	events := make(chan tunnel.Event, 8)
	var attempts atomic.Int32
	run := func(ctx context.Context, connected func(ip string)) error {
		n := attempts.Add(1)
		if n == 1 {
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

	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry after a healthy session with KeepAlive off)", got)
	}
}
