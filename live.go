package env

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
)

// Live wraps a [Loader] in an atomic-pointer-swapping live-reload
// container. After [NewLive] returns, [Live.Get] is lock-free and
// torn-read-free: every reload installs a fresh `*T` via
// [atomic.Pointer.Store], so readers always observe a complete,
// validated snapshot.
//
// Treat the struct returned by Get as IMMUTABLE — mutating it would
// race with future readers, defeating the atomic-swap design. The
// intended pattern is "read fields off Get() in a hot path"; for any
// state the reader itself mutates, copy out first.
type Live[T any] struct {
	loader *Loader
	curr   atomic.Pointer[T]

	cancel    context.CancelFunc
	eng       *engine
	closeOnce sync.Once
}

// NewLive constructs a [Loader] with the given options and performs
// an initial Load. Returns nil and the error on first-load failure.
// On success the returned Live runs a worker goroutine that reloads
// in response to source-change events and [Live.Reload] calls.
func NewLive[T any](opts ...Option) (*Live[T], error) {
	return NewLiveWithLoader[T](New(opts...))
}

// NewLiveWithLoader is like [NewLive] but uses an existing [*Loader].
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
	prevImmutable, immMeta, err := l.snapshotImmutable(tType)
	if err != nil {
		return nil, err
	}
	prevAll, err := l.snapshotKnownKeys(tType)
	if err != nil {
		return nil, err
	}
	redact := l.redactor()

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is invoked from Live.Close; the engine outlives this constructor.
	live.cancel = cancel

	reload := func(ctx context.Context, _ []string) reloadOutcome {
		var candidate T
		if err := l.LoadContext(ctx, &candidate); err != nil {
			return reloadOutcome{err: err}
		}
		newImmutable, _, err := l.snapshotImmutable(tType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		if mismatch := compareImmutable(prevImmutable, newImmutable, immMeta, redact); mismatch != nil {
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

// Get returns the most recently loaded value. Lock-free, torn-read-
// free, never nil after [NewLive] succeeds. Callers must treat the
// pointed-to struct as immutable; the worker may install a new
// pointer at any time, but every previously-handed-out pointer
// continues to reference a frozen value.
func (l *Live[T]) Get() *T {
	return l.curr.Load()
}

// Events returns the channel of [Event] values from the worker — one
// per reload attempt (success or failure). The channel is closed by
// [Live.Close].
//
// The channel is buffered (capacity 16); a slow consumer drops
// surplus events. Get() still returns the freshest value, so dropped
// events never hide a successful reload from a hot-path reader.
func (l *Live[T]) Events() <-chan Event {
	return l.eng.events
}

// Reload triggers a synchronous reload through the worker, bypassing
// the debounce timer. Returns the reload error (validation,
// immutable, decode) or nil on success.
func (l *Live[T]) Reload() error {
	return l.eng.requestReload()
}

// Close stops the worker, cancels every Watcher subscription, and
// closes the events channel. Idempotent. [Live.Get] continues to
// return the last-loaded value after Close.
func (l *Live[T]) Close() error {
	l.closeOnce.Do(func() {
		l.cancel()
		<-l.eng.doneCh
	})
	return nil
}
