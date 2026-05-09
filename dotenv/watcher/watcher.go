package watcher

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/go-rotini/env"
	"github.com/go-rotini/env/dotenv"
)

// Sentinel errors returned by the watcher.
var (
	// ErrEmptyPath is returned by [New] when path is the empty string.
	ErrEmptyPath = errors.New("watcher: path must not be empty")

	// ErrNilContext is returned by [Source.Watch] when the supplied
	// context is nil.
	ErrNilContext = errors.New("watcher: nil context")

	// ErrClosed is returned by [Source.Watch] when called on a Source
	// that has already been closed via [Source.Close].
	ErrClosed = errors.New("watcher: source closed")
)

// Source is a fsnotify-backed [env.Source] for a single `.env` file.
// It re-parses the file on disk changes and broadcasts an [env.Change]
// (carrying the keys that were added, modified, or removed) to every
// active subscriber returned by [Source.Watch].
//
// Source implements [env.Source] (so [Source.Lookup] can compose with
// the rest of the loader's source chain) and [env.Watcher] (so the
// loader's Watch / Live[T] machinery — Phase 11 — can subscribe to
// file-change events without polling).
//
// Source must be created with [New]; the zero value is not usable. Call
// [Source.Close] to release the underlying fsnotify resources.
type Source struct {
	absPath string
	parent  string
	name    string
	opts    options

	fsw *fsnotify.Watcher

	mu     sync.RWMutex
	values map[string]string

	subMu       sync.Mutex
	subscribers []chan env.Change

	closeOnce sync.Once
	closeCh   chan struct{}
	doneCh    chan struct{}
}

// Compile-time assertions.
var (
	_ env.Source  = (*Source)(nil)
	_ env.Watcher = (*Source)(nil)
)

// New constructs a [*Source] for the `.env` file at path. The parent
// directory is registered with fsnotify; events on other files in the
// same directory are filtered out. New performs an initial parse of the
// target file before returning, so the values are immediately available
// via [Source.Lookup]. The target file MUST exist at New time; for
// "create the .env later" workflows, write an empty file first.
//
// Callers must invoke [Source.Close] when finished to release the
// fsnotify watcher.
func New(path string, opts ...Option) (*Source, error) {
	if path == "" {
		return nil, ErrEmptyPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("watcher: resolve abs path: %w", err)
	}
	parent := filepath.Dir(abs)
	name := filepath.Base(abs)

	cfg := defaultOptions()
	for _, opt := range opts {
		opt(&cfg)
	}

	// Initial parse — surfaces "file missing" / "syntax error" eagerly.
	values, err := loadFile(abs, cfg.parseOptions)
	if err != nil {
		return nil, err
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watcher: create fsnotify: %w", err)
	}
	if err := fsw.Add(parent); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("watcher: watch parent dir %q: %w", parent, err)
	}

	s := &Source{
		absPath: abs,
		parent:  parent,
		name:    name,
		opts:    cfg,
		fsw:     fsw,
		values:  values,
		closeCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go s.run()
	return s, nil
}

// Path returns the absolute path of the watched file.
func (s *Source) Path() string { return s.absPath }

// Lookup implements [env.Source]. The returned value reflects the most
// recent successful parse of the target file. Concurrent with Lookup,
// the watcher's worker goroutine may install a fresh values map; Lookup
// is safe for concurrent use.
func (s *Source) Lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// Watch implements [env.Watcher]. Returns a channel that delivers an
// [env.Change] every time the target file changes on disk. The channel
// is closed when ctx is canceled OR when [Source.Close] is called,
// whichever fires first. Each call to Watch returns an independent
// subscription.
//
// The channel is buffered (capacity 1). If a subscriber is too slow to
// drain, subsequent change notifications during the backlog are dropped
// — the loader treats every Change as "re-read everything," so missing
// individual fan-out events is benign.
func (s *Source) Watch(ctx context.Context) (<-chan env.Change, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	select {
	case <-s.closeCh:
		return nil, ErrClosed
	default:
	}
	ch := make(chan env.Change, 1)
	s.subMu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.subMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-s.closeCh:
		}
		s.removeSubscriber(ch)
	}()
	return ch, nil
}

// Close stops the watcher and releases the underlying fsnotify resources.
// Calling Close more than once is safe; subsequent calls are no-ops. After
// Close, every active subscription channel is closed and Lookup continues
// to return the values from the last successful parse.
func (s *Source) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closeCh)
		if cerr := s.fsw.Close(); cerr != nil {
			err = fmt.Errorf("watcher: close fsnotify: %w", cerr)
		}
		<-s.doneCh
		s.subMu.Lock()
		for _, ch := range s.subscribers {
			close(ch)
		}
		s.subscribers = nil
		s.subMu.Unlock()
	})
	return err
}

// run is the worker goroutine. It owns the debouncer and the fsnotify
// channels exclusively; the watcher's mutable state (`values` and
// `subscribers`) is touched under their respective locks.
func (s *Source) run() {
	defer close(s.doneCh)

	deb := newDebouncer(s.opts.debounce)
	defer deb.stop()

	for {
		select {
		case ev, ok := <-s.fsw.Events:
			if !ok {
				return
			}
			if !s.matchesTarget(ev.Name) {
				continue
			}
			deb.tap()
		case <-deb.c():
			s.reload()
		case err, ok := <-s.fsw.Errors:
			if !ok {
				return
			}
			s.opts.logger.Warn("watcher: fsnotify error",
				"path", s.absPath, "err", err)
		case <-s.closeCh:
			return
		}
	}
}

// matchesTarget reports whether an fsnotify event applies to the target
// file. Comparison is by base name AND by full path: fsnotify on Linux
// reports the path as given to Add() (the parent directory) joined with
// the basename of the changed entry, but on macOS/Windows the absolute
// form may differ in normalization. Comparing both covers every
// platform.
func (s *Source) matchesTarget(p string) bool {
	if p == "" {
		return false
	}
	if filepath.Base(p) != s.name {
		return false
	}
	// Restrict to the parent directory we actually registered, so that
	// events for a same-named file in some sibling directory (which
	// fsnotify shouldn't report anyway) are ignored defensively.
	dir := filepath.Dir(p)
	if dir != s.parent && dir != "." {
		// "." appears when the caller passed a bare filename — accept.
		return false
	}
	return true
}

// reload re-parses the target file, computes the diff against the last
// successful parse, swaps the values map, and broadcasts an env.Change
// on every active subscription channel. If the re-parse fails the old
// values are retained and the failure is logged.
func (s *Source) reload() {
	values, err := loadFile(s.absPath, s.opts.parseOptions)
	if err != nil {
		s.opts.logger.Warn("watcher: reload failed",
			"path", s.absPath, "err", err)
		return
	}
	s.mu.Lock()
	old := s.values
	s.values = values
	s.mu.Unlock()

	keys := diffKeys(old, values)
	change := env.Change{Keys: keys}
	s.broadcast(change)
}

// broadcast delivers change to every active subscription channel using
// non-blocking sends. A channel that is full (the subscriber hasn't
// drained the previous Change yet) drops this fan-out; the loader's
// reload path treats Change events as idempotent, so the dropped event
// merely delays — never corrupts — the next reload.
func (s *Source) broadcast(change env.Change) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- change:
		default:
			s.opts.logger.Debug("watcher: subscriber channel full, dropped event",
				"path", s.absPath)
		}
	}
}

// removeSubscriber removes ch from the subscribers slice and closes it.
// Safe to call multiple times for the same channel; the second call is
// a no-op (the channel is already removed and closed).
func (s *Source) removeSubscriber(ch chan env.Change) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	idx := slices.Index(s.subscribers, ch)
	if idx < 0 {
		return
	}
	s.subscribers = slices.Delete(s.subscribers, idx, idx+1)
	close(ch)
}

// loadFile parses the dotenv file at path with the given parser options
// and returns its key/value map. Any parse error from dotenv.Load is
// returned unchanged; "file does not exist" is reported as the standard
// `*fs.PathError` from the underlying ReadFile.
func loadFile(path string, parseOpts []dotenv.ParseOption) (map[string]string, error) {
	m, err := dotenv.Load(path, parseOpts...)
	if err != nil {
		return nil, fmt.Errorf("watcher: parse %s: %w", path, err)
	}
	return m, nil
}

// diffKeys returns the sorted union of keys whose values differ between
// old and next (added, modified, or removed). The slice is empty when
// the two maps are equal — empty Keys still triggers a reload at the
// loader level (semantics: "the source signaled a change but did not
// identify which keys"), but the watcher prefers to emit a precise list
// when the diff is computable.
func diffKeys(old, next map[string]string) []string {
	if len(old) == 0 && len(next) == 0 {
		return nil
	}
	out := make([]string, 0)
	for k, v := range next {
		if oldV, ok := old[k]; !ok || oldV != v {
			out = append(out, k)
		}
	}
	for k := range old {
		if _, ok := next[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// snapshot returns a copy of the current values map (used by tests and
// for diagnostics). Concurrent-safe.
func (s *Source) snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.values)
}
