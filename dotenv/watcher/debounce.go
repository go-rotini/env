package watcher

import "time"

// debouncer collapses a burst of tap calls into a single channel send
// after delay of quiet. Owned by the watcher's worker goroutine; tap
// is called only from the worker, and the [time.AfterFunc] callback
// only writes to a buffered channel.
//
// delay==0 fires synchronously on every tap.
type debouncer struct {
	delay time.Duration
	fired chan struct{}
	timer *time.Timer
}

func newDebouncer(delay time.Duration) *debouncer {
	return &debouncer{
		delay: delay,
		fired: make(chan struct{}, 1),
	}
}

// tap reschedules the fire to delay from now, replacing any pending
// fire. With delay==0 the fire is delivered immediately (dropped
// non-blockingly when the channel is full).
func (d *debouncer) tap() {
	if d.delay == 0 {
		d.fire()
		return
	}
	if d.timer == nil {
		d.timer = time.AfterFunc(d.delay, d.fire)
		return
	}
	d.timer.Reset(d.delay)
}

func (d *debouncer) fire() {
	select {
	case d.fired <- struct{}{}:
	default:
	}
}

// c returns the channel that fires delay after the last tap.
func (d *debouncer) c() <-chan struct{} { return d.fired }

// stop releases the underlying timer. After stop, tap must not be
// called.
func (d *debouncer) stop() {
	if d.timer != nil {
		d.timer.Stop()
	}
}
