package env

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// Event is delivered on the channel returned by [Loader.Watch] or by
// [Live.Events]. One Event corresponds to one reload attempt —
// successful or failed.
type Event struct {
	// Changed lists the env-var names whose resolved values differ
	// from the snapshot taken before this reload, sorted and
	// deduplicated. Empty on a failed or no-op reload.
	Changed []string

	// Err is non-nil on failed reload; the previously-loaded value is
	// retained.
	Err error

	// Time is the wall-clock time the reload completed.
	Time time.Time
}

// ErrWatcherClosed is returned by [Live.Reload] when the worker
// goroutine has already exited.
var ErrWatcherClosed = errors.New("env: watcher closed")

// ErrNilContext is returned when a watch / load call receives a nil
// context.
var ErrNilContext = errors.New("env: nil context")

// Watch starts live reload. v must be a non-nil pointer to a struct
// (same constraints as [Loader.Load]). Watch performs an initial Load
// synchronously; on failure it returns a nil channel and the error.
//
// On every change pushed by a [Watcher] source — or on every poll
// tick when [WithPollInterval] is set — Watch builds a fresh
// candidate of v's type, runs the full load pipeline, compares the
// candidate's immutable-tagged values against the previous reload's
// snapshot, and on success overwrites *v. One [Event] is delivered
// per reload attempt; failures (validation, immutable, decode) deliver
// an Event with Err set and leave *v unmodified.
//
// The channel is closed when ctx is canceled.
//
// v must NOT be read concurrently with the worker's writes. For
// lock-free, torn-read-free reads use [NewLive] instead.
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

	prevImmutable, immMeta, err := l.snapshotImmutable(elemType)
	if err != nil {
		return nil, err
	}
	prevAll, err := l.snapshotKnownKeys(elemType)
	if err != nil {
		return nil, err
	}
	redact := l.redactor()

	reload := func(ctx context.Context, _ []string) reloadOutcome {
		candidate := reflect.New(elemType)
		if err := l.LoadContext(ctx, candidate.Interface()); err != nil {
			return reloadOutcome{err: err}
		}
		newImmutable, _, err := l.snapshotImmutable(elemType)
		if err != nil {
			return reloadOutcome{err: err}
		}
		if mismatch := compareImmutable(prevImmutable, newImmutable, immMeta, redact); mismatch != nil {
			return reloadOutcome{err: mismatch}
		}
		newAll, err := l.snapshotKnownKeys(elemType)
		if err != nil {
			return reloadOutcome{err: err}
		}
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

type reloadOutcome struct {
	changed []string
	err     error
}

// engine is the shared worker for [Loader.Watch] and [Live]. It
// subscribes to [Watcher] sources, optionally polls non-Watcher
// sources, and serializes reloads through a single goroutine. Manual
// requests from [Live.Reload] are dispatched through manualCh.
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

// eventLogger narrows what the engine needs from a [slog.Logger] so
// tests can substitute without depending on slog.
type eventLogger interface {
	Warn(msg string, args ...any)
	Debug(msg string, args ...any)
}

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

// forwardChanges relays one Watcher source's events onto sourceCh,
// dropping non-blockingly when sourceCh is full. Reloads are
// idempotent, so a dropped Change at most delays detection.
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

// run is the engine's serialization point: every reload happens here,
// on a single goroutine.
func (e *engine) run(ctx context.Context) {
	defer close(e.doneCh)
	defer close(e.events)

	debounce := e.loader.opts.reloadDebounce
	pendingKeys := make(map[string]struct{})

	var debTimer *time.Timer
	var debC <-chan time.Time
	armDeb := func() {
		if debounce == 0 {
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

// emit delivers ev to the events channel non-blockingly; surplus
// events are dropped (with a warning) when the consumer falls behind.
func (e *engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
		e.logger.Warn("env: event channel full, dropped event",
			"changed", ev.Changed, "err", ev.Err)
	}
}

// requestReload triggers a synchronous reload through the worker.
// Returns [ErrWatcherClosed] if the engine has already exited.
func (e *engine) requestReload() error {
	req := manualReloadReq{resp: make(chan error, 1)}
	select {
	case e.manualCh <- req:
		return <-req.resp
	case <-e.doneCh:
		return ErrWatcherClosed
	}
}

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

// immutableMeta is the per-key field metadata [snapshotImmutable]
// carries so [compareImmutable] can fill in field context and redact
// secret-tagged values in any [*ImmutableChangedError] it builds.
type immutableMeta struct {
	goPath string
	secret bool
}

// snapshotImmutable captures the resolved values of every
// immutable-tagged field of t, plus each key's field metadata. Unset /
// no-default keys take the [unsetSentinel] so the comparison can
// distinguish set-to-empty from missing. The returned meta map is a
// pure function of t (no source values) — capture it once.
func (l *Loader) snapshotImmutable(t reflect.Type) (values map[string]string, meta map[string]immutableMeta, err error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	info, err := l.structInfo(t)
	if err != nil {
		return nil, nil, err
	}
	src := l.composedSource()
	values = make(map[string]string, len(info.Fields))
	meta = make(map[string]immutableMeta, len(info.Fields))
	for _, fd := range info.Fields {
		if !fd.Immutable {
			continue
		}
		key := l.opts.prefix + fd.Key
		meta[key] = immutableMeta{goPath: fd.GoPath, secret: fd.Secret}
		if v, ok := src.Lookup(key); ok {
			values[key] = v
		} else if fd.HasDefault {
			values[key] = fd.DefaultValue
		} else {
			values[key] = unsetSentinel
		}
	}
	return values, meta, nil
}

// snapshotKnownKeys captures the resolved values of every field of t
// for change detection across reloads.
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

// unsetSentinel marks "no source value, no default" so the immutable
// comparison treats "" → unset and unset → "" as real changes.
const unsetSentinel = "\x00<unset>\x00"

// compareImmutable returns a non-nil [*ImmutableChangedError] when next
// differs from prev on any immutable key. Old/New are made human-
// readable (the unset sentinel renders as "<unset>") and, for
// secret-tagged fields, run through redact.
func compareImmutable(prev, next map[string]string, meta map[string]immutableMeta, redact func(string) string) error {
	mkErr := func(key, oldV, newV string) error {
		m := meta[key]
		return &ImmutableChangedError{
			Field: m.goPath,
			Key:   key,
			Old:   displayImmutable(oldV, m.secret, redact),
			New:   displayImmutable(newV, m.secret, redact),
		}
	}
	for key, prevVal := range prev {
		newVal, ok := next[key]
		if !ok {
			return mkErr(key, prevVal, unsetSentinel)
		}
		if newVal != prevVal {
			return mkErr(key, prevVal, newVal)
		}
	}
	for key, newVal := range next {
		if _, ok := prev[key]; !ok {
			return mkErr(key, unsetSentinel, newVal)
		}
	}
	return nil
}

// displayImmutable renders an immutable snapshot value for an error
// message: the unset sentinel becomes "<unset>", and a secret-tagged
// value is run through redact.
func displayImmutable(v string, secret bool, redact func(string) string) string {
	if v == unsetSentinel {
		return "<unset>"
	}
	if secret {
		return redact(v)
	}
	return v
}

// diffSnapshots returns the sorted union of keys whose resolved value
// differs between prev and next.
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
