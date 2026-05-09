package watcher

import "time"

// debouncer collapses a burst of `tap()` calls into a single channel-send
// after `delay` of quiet. Calling `tap()` resets the timer; the
// debouncer's channel fires once after `delay` has elapsed since the
// most recent tap.
//
// A debouncer with delay==0 fires synchronously on every tap (no
// debouncing).
//
// debouncer is owned by a single goroutine (the watcher's worker loop):
// `tap()` is called only from the worker's event handling, and `c()` is
// drained only from the worker's select. The internal time.AfterFunc
// callback runs on its own goroutine but only sends to an already-
// constructed buffered channel, so no external synchronization is
// required for that path.
type debouncer struct {
	delay time.Duration
	fired chan struct{}
	timer *time.Timer
}

// newDebouncer returns a debouncer with the given delay. The returned
// debouncer is created in the "stopped" state — the channel does not
// fire until the first `tap()` after construction.
func newDebouncer(delay time.Duration) *debouncer {
	return &debouncer{
		delay: delay,
		fired: make(chan struct{}, 1),
	}
}

// tap resets the debouncer's timer to fire after `delay` from now. If
// the timer is already pending, the existing pending fire is canceled
// and rescheduled. With delay==0, the debouncer fires immediately
// (non-blocking — if the channel is full the tap is silently dropped,
// since the receiver hasn't yet processed the previous fire).
func (d *debouncer) tap() {
	if d.delay == 0 {
		d.fire()
		return
	}
	if d.timer == nil {
		d.timer = time.AfterFunc(d.delay, d.fire)
		return
	}
	// Per Go 1.23 timer semantics, Reset is safe to call on a pending,
	// expired, or already-stopped timer; the function is rescheduled
	// (or scheduled) for `delay` from now.
	d.timer.Reset(d.delay)
}

// fire delivers a single notification on the channel, dropping if the
// channel is already full. Called either from `tap()` (delay==0) or
// from the time.AfterFunc callback goroutine (delay>0).
func (d *debouncer) fire() {
	select {
	case d.fired <- struct{}{}:
	default:
	}
}

// c returns the channel that fires `delay` after the last tap.
func (d *debouncer) c() <-chan struct{} { return d.fired }

// stop releases the debouncer's underlying timer. After stop, `tap`
// must not be called.
func (d *debouncer) stop() {
	if d.timer != nil {
		d.timer.Stop()
	}
}
