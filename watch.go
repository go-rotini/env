package env

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// Event is one element delivered on the channel returned by
// [Loader.Watch] or by [Live.Events]. Each Event corresponds to one
// reload attempt — successful or failed.
type Event struct {
	// Changed lists the env-var names whose resolved values differ from
	// the snapshot taken before this reload. The list is sorted and
	// deduplicated. On a failed reload (Err != nil) Changed is empty;
	// on a successful but no-op reload Changed is also empty.
	Changed []string

	// Err is non-nil when the reload failed. The previously-loaded
	// value is retained on failure.
	Err error

	// Time is the wall-clock time the reload completed.
	Time time.Time
}

// ErrWatcherClosed is returned by [Live.Reload] when called on a Live
// whose worker goroutine has already exited.
var ErrWatcherClosed = errors.New("env: watcher closed")

// ErrNilContext is returned when a watch / load call receives a nil
// context. Surfaces from [Loader.Watch] and friends; callers can match
// it via [errors.Is].
var ErrNilContext = errors.New("env: nil context")

// Watch begins live reload. The supplied v must be a non-nil pointer to
// a struct (the same shape constraints as [Loader.Load] / [Loader.LoadContext]).
// Watch performs an initial Load synchronously; if that fails the
// returned channel is nil and the error is returned.
//
// On every subsequent change pushed by a [Watcher]-implementing source
// — or every poll tick if [WithPollInterval] is configured — Watch
// builds a fresh candidate of the same type as v, runs the full load
// pipeline (mutators, validation, fromFile, decode), compares the new
// candidate's `immutable`-tagged values against the previous reload's
// snapshot, and (on success) overwrites *v with the candidate values.
// One [Event] is delivered to the returned channel per reload attempt.
//
// The channel is closed when ctx is canceled. Failures (validation
// errors, immutable-field changes, decode errors) deliver an Event with
// a non-nil Err and leave *v unmodified.
//
// v MUST NOT be read concurrently with the worker's writes; readers who
// need lock-free, torn-read-free access should use [NewLive] instead,
// which atomically swaps an internal pointer.
func (l *Loader) Watch(ctx context.Context, v any) (<-chan Event, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := l.LoadContext(ctx, v); err != nil {
		return nil, err
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, fmt.Errorf("env.Watch: %w", ErrNilPointer)
	}
	elemType := rv.Elem().Type()

	prevImmutable, err := l.snapshotImmutable(elemType)
	if err != nil {
		return nil, err
	}
	prevAll, err := l.snapshotKnownKeys(elemType)
	if err != nil {
		return nil, err
	}

	reload := func(ctx context.Context, _ []string) reloadOutcome {
		candidate := reflect.New(elemType)
		if err := l.LoadContext(ctx, candidate.Interface()); err != nil {
			return reloadOutcome{err: err}
		}
		newImmutable, err := l.snapshotImmutable(elemType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		if mismatch := compareImmutable(prevImmutable, newImmutable); mismatch != nil {
			return reloadOutcome{err: mismatch}
		}
		newAll, err := l.snapshotKnownKeys(elemType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		// Commit: copy candidate → v.
		rv.Elem().Set(candidate.Elem())
		changed := diffSnapshots(prevAll, newAll)
		prevAll = newAll
		prevImmutable = newImmutable
		return reloadOutcome{changed: changed}
	}

	eng := newEngine(l, reload)
	eng.start(ctx)
	return eng.events, nil
}

// reloadOutcome is the result of one engine-driven reload attempt.
type reloadOutcome struct {
	changed []string
	err     error
}

// engine is the shared worker that drives both [Loader.Watch] and
// [Live]. It subscribes to every [Watcher]-implementing source in the
// loader's chain, optionally polls non-Watcher sources, and serializes
// reload calls through a single goroutine. Manual reload requests
// (used by [Live.Reload]) are dispatched through manualCh.
type engine struct {
	loader *Loader
	events chan Event
	reload func(ctx context.Context, sourceKeys []string) reloadOutcome

	sourceCh chan Change
	manualCh chan manualReloadReq

	doneCh chan struct{}

	logger eventLogger
}

type manualReloadReq struct {
	resp chan error
}

// eventLogger interface narrows what the engine needs from a slog.Logger
// so tests can replace it without bringing in slog. Discard logger is
// the default when no [WithLogger] was applied.
type eventLogger interface {
	Warn(msg string, args ...any)
	Debug(msg string, args ...any)
}

// nopLogger is the eventLogger used when WithLogger isn't set.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}

func newEngine(l *Loader, reload func(ctx context.Context, sourceKeys []string) reloadOutcome) *engine {
	var logger eventLogger = nopLogger{}
	if l.opts.logger != nil {
		logger = l.opts.logger
	}
	return &engine{
		loader:   l,
		events:   make(chan Event, 16),
		reload:   reload,
		sourceCh: make(chan Change, 16),
		manualCh: make(chan manualReloadReq),
		doneCh:   make(chan struct{}),
		logger:   logger,
	}
}

// start spawns the engine's worker goroutine and the per-source
// forwarder goroutines.
func (e *engine) start(ctx context.Context) {
	for _, src := range e.loader.opts.sources {
		w, ok := src.(Watcher)
		if !ok {
			continue
		}
		sub, err := w.Watch(ctx)
		if err != nil {
			e.logger.Warn("env: watcher source subscribe failed", "err", err)
			continue
		}
		go e.forwardChanges(ctx, sub)
	}
	go e.run(ctx)
}

// forwardChanges relays Change events from one Watcher source onto the
// engine's central source channel. Exits when sub is closed or ctx is
// canceled. Non-blocking forwarding: if the central channel is full,
// the change is dropped (the engine's debounce + idempotent reload
// model treats every Change as "may have changed"; dropping individual
// events at most delays detection of the keys that triggered them).
func (e *engine) forwardChanges(ctx context.Context, sub <-chan Change) {
	for {
		select {
		case ch, ok := <-sub:
			if !ok {
				return
			}
			select {
			case e.sourceCh <- ch:
			case <-ctx.Done():
				return
			default:
				e.logger.Debug("env: dropped change (engine source channel full)")
			}
		case <-ctx.Done():
			return
		}
	}
}

// run is the engine's serialization point. All reload work happens
// here, on a single goroutine; this is what makes the immutable-field
// comparison and the atomic-pointer store race-free.
func (e *engine) run(ctx context.Context) {
	defer close(e.doneCh)
	defer close(e.events)

	debounce := e.loader.opts.reloadDebounce
	pendingKeys := make(map[string]struct{})

	var debTimer *time.Timer
	var debC <-chan time.Time
	armDeb := func() {
		if debounce == 0 {
			// Synchronous fire path — handled inline in the caller.
			return
		}
		if debTimer == nil {
			debTimer = time.NewTimer(debounce)
			debC = debTimer.C
			return
		}
		if !debTimer.Stop() {
			select {
			case <-debTimer.C:
			default:
			}
		}
		debTimer.Reset(debounce)
		debC = debTimer.C
	}

	var pollC <-chan time.Time
	if e.loader.opts.pollInterval > 0 {
		t := time.NewTicker(e.loader.opts.pollInterval)
		defer t.Stop()
		pollC = t.C
	}

	fire := func() {
		keys := keysOf(pendingKeys)
		pendingKeys = make(map[string]struct{})
		out := e.reload(ctx, keys)
		e.emit(Event{
			Changed: out.changed,
			Err:     out.err,
			Time:    time.Now(),
		})
	}

	for {
		select {
		case <-ctx.Done():
			if debTimer != nil {
				debTimer.Stop()
			}
			return

		case ch, ok := <-e.sourceCh:
			if !ok {
				continue
			}
			for _, k := range ch.Keys {
				pendingKeys[k] = struct{}{}
			}
			if debounce == 0 {
				fire()
				continue
			}
			armDeb()

		case <-pollC:
			if debounce == 0 {
				fire()
				continue
			}
			armDeb()

		case <-debC:
			fire()
			debC = nil

		case req := <-e.manualCh:
			if debTimer != nil {
				debTimer.Stop()
				debC = nil
			}
			out := e.reload(ctx, keysOf(pendingKeys))
			pendingKeys = make(map[string]struct{})
			e.emit(Event{
				Changed: out.changed,
				Err:     out.err,
				Time:    time.Now(),
			})
			req.resp <- out.err
		}
	}
}

// emit delivers ev to the events channel using a non-blocking send. If
// the channel is full (a slow consumer is falling behind) the event is
// dropped and a warning is logged. Buffered to 16 — comfortable headroom
// for the typical "event observer" goroutine pattern.
func (e *engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
		e.logger.Warn("env: event channel full, dropped event",
			"changed", ev.Changed, "err", ev.Err)
	}
}

// requestReload triggers a synchronous reload through the worker. Used
// by [Live.Reload]. Returns [ErrWatcherClosed] if the engine has
// already exited.
func (e *engine) requestReload() error {
	req := manualReloadReq{resp: make(chan error, 1)}
	select {
	case e.manualCh <- req:
		return <-req.resp
	case <-e.doneCh:
		return ErrWatcherClosed
	}
}

// keysOf returns the sorted slice of keys in m. Used to convert the
// engine's pending-key set into the slice handed off to the reload
// closure.
func keysOf(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// snapshotImmutable returns the resolved values for every immutable-
// tagged field in the type t, keyed by the env-var name (with
// [WithPrefix] applied). Reads from the loader's current source chain.
// A field whose source returns ok=false and which has no default is
// represented by the sentinel value `<unset>` to distinguish "set to
// empty" (allowed across reloads) from "missing" (also allowed, but
// distinct).
func (l *Loader) snapshotImmutable(t reflect.Type) (map[string]string, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	info, err := l.structInfo(t)
	if err != nil {
		return nil, err
	}
	src := l.composedSource()
	out := make(map[string]string, len(info.Fields))
	for _, fd := range info.Fields {
		if !fd.Immutable {
			continue
		}
		key := l.opts.prefix + fd.Key
		if v, ok := src.Lookup(key); ok {
			out[key] = v
			continue
		}
		if fd.HasDefault {
			out[key] = fd.DefaultValue
			continue
		}
		out[key] = unsetSentinel
	}
	return out, nil
}

// snapshotKnownKeys returns the resolved values for EVERY field in t,
// for change-detection across reloads.
func (l *Loader) snapshotKnownKeys(t reflect.Type) (map[string]string, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	info, err := l.structInfo(t)
	if err != nil {
		return nil, err
	}
	src := l.composedSource()
	out := make(map[string]string, len(info.Fields))
	for _, fd := range info.Fields {
		key := l.opts.prefix + fd.Key
		if v, ok := src.Lookup(key); ok {
			out[key] = v
			continue
		}
		if fd.HasDefault {
			out[key] = fd.DefaultValue
			continue
		}
		out[key] = unsetSentinel
	}
	return out, nil
}

// unsetSentinel is the marker placed in immutable / known-key snapshots
// for keys that are not set by any source AND have no default. Using a
// distinct sentinel lets the immutable comparison treat
// "<unset>" → "" and "" → "<unset>" as real changes (they are: the
// field went from "no value, falls back to zero" to "explicitly empty",
// which the loader treats as decode-with-empty).
const unsetSentinel = "\x00<unset>\x00"

// compareImmutable returns a non-nil [*ImmutableChangedError] when the
// new snapshot differs from the previous on any immutable key. Returns
// nil when every immutable key is unchanged.
func compareImmutable(prev, next map[string]string) error {
	for key, prevVal := range prev {
		newVal, ok := next[key]
		if !ok || newVal != prevVal {
			return &ImmutableChangedError{
				Key: key,
				Old: prevVal,
				New: newVal,
			}
		}
	}
	for key, newVal := range next {
		if _, ok := prev[key]; !ok {
			return &ImmutableChangedError{
				Key: key,
				New: newVal,
			}
		}
	}
	return nil
}

// diffSnapshots returns the sorted union of keys whose resolved value
// differs between prev and next. Used to populate Event.Changed.
func diffSnapshots(prev, next map[string]string) []string {
	out := make([]string, 0)
	for k, v := range next {
		if pv, ok := prev[k]; !ok || pv != v {
			out = append(out, k)
		}
	}
	for k := range prev {
		if _, ok := next[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
