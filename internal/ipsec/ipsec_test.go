package ipsec

import (
	"context"
	"errors"
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
