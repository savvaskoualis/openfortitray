package uidispatch

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostRunsOnDrain(t *testing.T) {
	q := New()
	var ran atomic.Bool
	q.Post(func() { ran.Store(true) })
	if ran.Load() {
		t.Fatal("Post must not run synchronously")
	}
	q.Drain()
	if !ran.Load() {
		t.Fatal("Drain must run the posted func")
	}
}

func TestDrainRunsInPostOrder(t *testing.T) {
	q := New()
	var order []int
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		i := i
		q.Post(func() {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		})
	}
	q.Drain()
	for i, v := range order {
		if v != i {
			t.Fatalf("order = %v, want 0..4 in sequence", order)
		}
	}
}

func TestDrainWithNothingPostedIsANoop(t *testing.T) {
	q := New()
	q.Drain() // must not panic or block
}

func TestPostAndWaitBlocksUntilDrained(t *testing.T) {
	q := New()
	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		q.PostAndWait(func() { ran.Store(true) })
		close(done)
	}()

	// PostAndWait's caller must still be blocked before Drain runs.
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("PostAndWait returned before Drain ran the func")
	default:
	}

	q.Drain()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PostAndWait did not unblock after Drain")
	}
	if !ran.Load() {
		t.Fatal("PostAndWait's func did not run")
	}
}

func TestPostIsSafeFromManyGoroutines(t *testing.T) {
	q := New()
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Post(func() { count.Add(1) })
		}()
	}
	wg.Wait()
	q.Drain()
	if count.Load() != 100 {
		t.Fatalf("count = %d, want 100", count.Load())
	}
}
