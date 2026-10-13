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
// successful or failed — except that a poll that changes nothing
// delivers none (unless the previous reload failed), and a [Watcher] source that fails to subscribe
// delivers one with Err set.
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

// ErrNilContext is returned when [Loader.LoadContext] or [Loader.Watch]
// receives a nil context.
var ErrNilContext = errors.New("env: nil context")

// Watch starts live reload. v must be a non-nil pointer to a struct
// (same constraints as [Loader.Load]). Watch performs an initial Load
// synchronously; on failure it returns a nil channel and the error.
//
// On every change pushed by a [Watcher] source — or on every poll
// tick when [WithPollInterval] is set — Watch builds a fresh
// candidate of v's type, runs the full load pipeline (mutators and
// fromFile reads included), compares the candidate's immutable-tagged
// values against the previous load, and on success overwrites *v. One
// [Event] is delivered per reload attempt; failures (validation,
// immutable, decode) deliver an Event with Err set and leave *v
// unmodified. A poll-triggered reload that succeeds without changing
// any value delivers no Event, unless the previous reload failed. A [Watcher] source whose subscription
// fails is reported as an Event with Err set.
//
// Values of unset-tagged fields that the first load removed from the
// process environment are kept and reused by every reload.
//
// The channel is closed when ctx is canceled.
//
// v must NOT be read concurrently with the worker's writes. For
// lock-free, torn-read-free reads use [NewLive] instead.
func (l *Loader) Watch(ctx context.Context, v any) (<-chan Event, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	tr, err := l.newTracker(ctx, v)
	if err != nil {
		return nil, err
	}
	rv := reflect.ValueOf(v)
	elemType := rv.Elem().Type()

	reload := func(ctx context.Context, _ []string) reloadOutcome {
		candidate := reflect.New(elemType)
		changed, err := tr.load(ctx, candidate.Interface())
		if err != nil {
			return reloadOutcome{err: err}
		}
		rv.Elem().Set(candidate.Elem())
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
			e.emit(Event{Err: fmt.Errorf("env: watcher source subscribe: %w", err), Time: time.Now()})
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
	// maxWait bounds how long a steady stream of Watcher changes can
	// postpone a reload.
	maxWait := maxDebounceWaits * debounce
	pendingKeys := make(map[string]struct{})

	var debTimer *time.Timer
	var debC <-chan time.Time
	var burstStart time.Time
	stopDeb := func() {
		if debTimer != nil {
			debTimer.Stop()
		}
		debC = nil
	}

	var pollC <-chan time.Time
	if e.loader.opts.pollInterval > 0 {
		t := time.NewTicker(e.loader.opts.pollInterval)
		defer t.Stop()
		pollC = t.C
	}

	reloadNow := func() reloadOutcome {
		stopDeb()
		keys := keysOf(pendingKeys)
		pendingKeys = make(map[string]struct{})
		return e.reload(ctx, keys)
	}
	lastFailed := false // whether the last published reload failed
	publish := func(out reloadOutcome) {
		lastFailed = out.err != nil
		e.emit(Event{
			Changed: out.changed,
			Err:     out.err,
			Time:    time.Now(),
		})
	}

	for {
		select {
		case <-ctx.Done():
			stopDeb()
			return

		case ch, ok := <-e.sourceCh:
			if !ok {
				continue
			}
			for _, k := range ch.Keys {
				pendingKeys[k] = struct{}{}
			}
			if debounce == 0 {
				publish(reloadNow())
				continue
			}
			now := time.Now()
			if debC == nil {
				burstStart = now
			} else if now.Sub(burstStart) >= maxWait {
				publish(reloadNow())
				continue
			}
			if debTimer == nil {
				debTimer = time.NewTimer(debounce)
			} else {
				debTimer.Reset(debounce)
			}
			debC = debTimer.C

		case <-pollC:
			// A tick already ends a quiet period; debouncing it would
			// starve reloads when the interval is at or below the
			// debounce. A quiet poll emits no Event unless it follows a
			// failed reload, so consumers see the failure clear.
			if out := reloadNow(); out.err != nil || len(out.changed) > 0 || lastFailed {
				publish(out)
			}

		case <-debC:
			publish(reloadNow())

		case req := <-e.manualCh:
			out := reloadNow()
			publish(out)
			req.resp <- out.err
		}
	}
}

// maxDebounceWaits is how many debounce periods a burst of Watcher
// changes may last before the engine reloads anyway.
const maxDebounceWaits = 10

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

// immutableMeta is the per-key field metadata [compareImmutable] uses
// to fill in field context and redact values in any
// [*ImmutableChangedError] it builds.
type immutableMeta struct {
	goPath string
	redact bool
}

// reloadTracker carries what one [Loader.Watch] or [Live] compares
// across reloads. It is used only by the engine goroutine after the
// initial load.
type reloadTracker struct {
	l         *Loader
	prev      map[string]string // the values of the last installed load
	retained  map[string]string // see loadState.retained
	immutable map[string]immutableMeta
	redact    func(string) string
}

// newTracker performs the initial load into v and returns a tracker
// seeded from it.
func (l *Loader) newTracker(ctx context.Context, v any) (*reloadTracker, error) {
	tr := &reloadTracker{l: l, retained: make(map[string]string), redact: l.redactor()}
	st := &loadState{values: make(map[string]string), retained: tr.retained}
	if err := l.load(ctx, v, st); err != nil {
		return nil, err
	}
	info, keys := l.fields(reflect.TypeOf(v).Elem())
	tr.immutable = make(map[string]immutableMeta)
	for i, fd := range info.Fields {
		if fd.Immutable {
			tr.immutable[l.key(keys, i, fd)] = immutableMeta{goPath: fd.GoPath, redact: fd.Secret || fd.FromFile || fd.Expand || len(l.opts.mutators) > 0}
		}
	}
	tr.prev = st.values
	return tr, nil
}

// load loads a reload candidate into v, rejects it when an immutable
// field's value changed, and otherwise records it as the installed
// state and returns the keys whose values changed. The comparison uses
// the very strings the candidate was decoded from.
func (tr *reloadTracker) load(ctx context.Context, v any) ([]string, error) {
	st := &loadState{values: make(map[string]string, len(tr.prev)), retained: tr.retained}
	if err := tr.l.load(ctx, v, st); err != nil {
		return nil, err
	}
	if err := compareImmutable(tr.prev, st.values, tr.immutable, tr.redact); err != nil {
		return nil, err
	}
	changed := diffSnapshots(tr.prev, st.values)
	tr.prev = st.values
	return changed, nil
}

// unsetSentinel marks "no source value, no default" so the immutable
// comparison treats "" → unset and unset → "" as real changes.
const unsetSentinel = "\x00<unset>\x00"

// compareImmutable returns a non-nil [*ImmutableChangedError] when next
// differs from prev on an immutable key, checking keys in sorted order.
// Old/New are made human-readable (the unset sentinel renders as
// "<unset>") and, for fields marked to redact, run through redact.
func compareImmutable(prev, next map[string]string, meta map[string]immutableMeta, redact func(string) string) error {
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		oldV, newV := snapshotValue(prev, key), snapshotValue(next, key)
		if oldV == newV {
			continue
		}
		m := meta[key]
		return &ImmutableChangedError{
			Field: m.goPath,
			Key:   key,
			Old:   displayImmutable(oldV, m.redact, redact),
			New:   displayImmutable(newV, m.redact, redact),
		}
	}
	return nil
}

func snapshotValue(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return unsetSentinel
}

// displayImmutable renders an immutable value for an error message: the
// unset sentinel becomes "<unset>", and a redacted field's value is run
// through redact.
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
