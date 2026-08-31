// Package uidispatch marshals work from background goroutines onto a
// single-threaded UI loop. It replaces Fyne's fyne.Do/fyne.DoAndWait: instead
// of an arbitrary goroutine calling synchronously into the toolkit (the
// mechanism a blocking glfw.PollEvents call could wedge forever), every
// UI mutation is queued here and drained by a QTimer on the Qt main thread.
// There is no code path where an external event (display wake, a tunnel
// callback) blocks on a call into Qt itself.
package uidispatch

import "sync"

// Queue is a FIFO of pending UI-thread work. The zero value is not usable;
// construct with New.
type Queue struct {
	mu  sync.Mutex
	fns []func()
}

// New returns an empty Queue.
func New() *Queue {
	return &Queue{}
}

// Post queues fn to run on the next Drain. Safe to call from any goroutine.
// Never blocks.
func (q *Queue) Post(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
}

// PostAndWait queues fn and blocks the calling goroutine until a Drain has
// actually run it. Callers must never call this from the same thread that
// calls Drain (that would deadlock) — it exists for OS-notification
// callbacks (display wake, signal handlers) that need the UI mutation to be
// visibly complete before they return control to the OS.
func (q *Queue) PostAndWait(fn func()) {
	done := make(chan struct{})
	q.Post(func() {
		fn()
		close(done)
	})
	<-done
}

// Drain runs every function queued since the last Drain, in the order they
// were posted. Must only be called from the UI thread (the QTimer callback).
func (q *Queue) Drain() {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()

	for _, fn := range fns {
		fn()
	}
}
