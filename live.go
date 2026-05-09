package env

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
)

// Live wraps a [Loader] in an atomic-pointer-swapping live-reload
// container. After [NewLive] returns, [Live.Get] is lock-free and
// torn-read-free regardless of how many reloads happen concurrently —
// every reload installs a fresh `*T` via [atomic.Pointer.Store], so
// readers always observe a stable, consistent snapshot.
//
// Treat the struct returned by Get as IMMUTABLE. Mutating it would
// race with future readers, defeating the entire point of the atomic-
// swap design. The intended pattern is "read fields off Get() in a hot
// path"; for any state that the reader itself mutates, copy out first.
type Live[T any] struct {
	loader *Loader
	curr   atomic.Pointer[T]

	cancel    context.CancelFunc
	eng       *engine
	closeOnce sync.Once
}

// NewLive constructs a [Loader] with the given options and performs an
// initial Load. If the initial load fails NewLive returns nil and the
// error; no Live is constructed.
//
// On success the returned Live has an active worker goroutine that
// reloads in response to source-change events (and to manual
// [Live.Reload] calls); cancel it via [Live.Close].
func NewLive[T any](opts ...Option) (*Live[T], error) {
	return NewLiveWithLoader[T](New(opts...))
}

// NewLiveWithLoader is like [NewLive] but uses an existing [*Loader]
// rather than constructing one from options. Useful when the same
// loader configuration must drive multiple Live instances or when the
// loader is built once at program startup.
func NewLiveWithLoader[T any](l *Loader) (*Live[T], error) {
	if l == nil {
		return nil, ErrNilPointer
	}
	var initial T
	if err := l.Load(&initial); err != nil {
		return nil, err
	}
	live := &Live[T]{loader: l}
	live.curr.Store(&initial)

	tType := reflect.TypeOf(initial)
	prevImmutable, err := l.snapshotImmutable(tType)
	if err != nil {
		return nil, err
	}
	prevAll, err := l.snapshotKnownKeys(tType)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is invoked from Live.Close; the engine outlives this constructor.
	live.cancel = cancel

	reload := func(ctx context.Context, _ []string) reloadOutcome {
		var candidate T
		if err := l.LoadContext(ctx, &candidate); err != nil {
			return reloadOutcome{err: err}
		}
		newImmutable, err := l.snapshotImmutable(tType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		if mismatch := compareImmutable(prevImmutable, newImmutable); mismatch != nil {
			return reloadOutcome{err: mismatch}
		}
		newAll, err := l.snapshotKnownKeys(tType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		live.curr.Store(&candidate)
		changed := diffSnapshots(prevAll, newAll)
		prevAll = newAll
		prevImmutable = newImmutable
		return reloadOutcome{changed: changed}
	}

	live.eng = newEngine(l, reload)
	live.eng.start(ctx)
	return live, nil
}

// Get returns the most recently successfully-loaded value. Lock-free
// and torn-read-free; safe to call from any goroutine. The returned
// pointer is never nil after a successful [NewLive] / [NewLiveWithLoader].
//
// Callers MUST treat the pointed-to struct as immutable; the worker
// goroutine may install a new pointer at any time, but the old pointer
// continues to point at a frozen, never-mutated value.
func (l *Live[T]) Get() *T {
	return l.curr.Load()
}

// Events returns the channel of [Event] values emitted by the worker.
// One event per reload attempt (success or failure). The channel is
// closed when [Live.Close] is called.
//
// The channel is buffered (capacity 16). If a consumer is too slow to
// drain, surplus events are dropped — the next Get() still returns the
// freshest value, so backpressure-induced event loss never hides a
// successful reload from a hot-path reader.
func (l *Live[T]) Events() <-chan Event {
	return l.eng.events
}

// Reload triggers a synchronous reload through the worker, bypassing
// the debounce timer. Returns the reload error (validation failure,
// immutable-field change, decode error) or nil on success.
//
// Useful for sources that don't push change events (no [Watcher]
// implementation and no [WithPollInterval] configured): callers signal
// "I have a reason to believe the source changed; please re-read now".
func (l *Live[T]) Reload() error {
	return l.eng.requestReload()
}

// Close stops the worker, cancels every Watcher subscription, and
// drains the events channel. Safe to call multiple times; the second
// call is a no-op. After Close, [Live.Get] continues to return the
// last-loaded value.
func (l *Live[T]) Close() error {
	l.closeOnce.Do(func() {
		l.cancel()
		<-l.eng.doneCh
	})
	return nil
}
