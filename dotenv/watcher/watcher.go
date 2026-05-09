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

var (
	// ErrEmptyPath is returned by [New] when path is empty.
	ErrEmptyPath = errors.New("watcher: path must not be empty")

	// ErrNilContext is returned by [Source.Watch] for a nil context.
	ErrNilContext = errors.New("watcher: nil context")

	// ErrClosed is returned by [Source.Watch] after [Source.Close].
	ErrClosed = errors.New("watcher: source closed")
)

// Source is a fsnotify-backed [env.Source] for a single `.env` file.
// It re-parses on disk changes and broadcasts an [env.Change] to every
// active subscriber. Implements [env.Source] (composes into the
// loader's source chain) and [env.Watcher] (delivers push events to
// [env.Loader.Watch] and [env.Live]).
//
// Construct with [New]; the zero value is not usable. Call
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

var (
	_ env.Source  = (*Source)(nil)
	_ env.Watcher = (*Source)(nil)
)

// New constructs a [*Source] for the `.env` file at path. The parent
// directory is registered with fsnotify and events on sibling files
// are filtered out. The file must exist at New time — for "create
// later" workflows, write an empty file first. Callers must invoke
// [Source.Close] to release the fsnotify watcher.
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

	// Eager parse so missing-file / syntax errors surface from New.
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

// Lookup implements [env.Source] and returns the value from the most
// recent successful parse. Safe for concurrent use.
func (s *Source) Lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// Watch implements [env.Watcher] and returns a buffered (cap=1)
// channel that delivers an [env.Change] every time the target file
// changes. The channel closes when ctx is canceled or [Source.Close]
// runs. Each call to Watch returns an independent subscription.
// Surplus events on a slow subscriber are dropped; reloads are
// idempotent.
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

// Close stops the watcher and releases its fsnotify resources.
// Idempotent. Active subscription channels are closed; [Source.Lookup]
// continues to return the values from the last successful parse.
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

// run is the worker goroutine. It owns the debouncer and the
// fsnotify channels; mutable state is guarded by the receiver's
// locks.
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

// matchesTarget reports whether the fsnotify event at path p applies
// to the watched file. Both the basename and the parent directory are
// checked; "." appears when the caller passed a bare filename.
func (s *Source) matchesTarget(p string) bool {
	if p == "" {
		return false
	}
	if filepath.Base(p) != s.name {
		return false
	}
	dir := filepath.Dir(p)
	if dir != s.parent && dir != "." {
		return false
	}
	return true
}

// reload re-parses the file, swaps the values map under the write
// lock, and broadcasts an [env.Change] for the diff. Failures are
// logged; the old values are retained.
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

// broadcast delivers change to every active subscriber non-
// blockingly; full channels drop the fan-out (reloads are idempotent).
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

// removeSubscriber removes ch from the subscribers slice and closes
// it. Idempotent.
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

func loadFile(path string, parseOpts []dotenv.ParseOption) (map[string]string, error) {
	m, err := dotenv.Load(path, parseOpts...)
	if err != nil {
		return nil, fmt.Errorf("watcher: parse %s: %w", path, err)
	}
	return m, nil
}

// diffKeys returns the sorted union of keys whose values differ
// between old and next.
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

// snapshot returns a copy of the current values map.
func (s *Source) snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.values)
}
