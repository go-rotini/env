package watcher

import (
	"log/slog"
	"time"

	"github.com/go-rotini/env/dotenv"
)

// DefaultDebounce is the default quiet period after the last fsnotify
// event before re-parsing. 75ms covers the burst patterns produced by
// editor save sequences (write-temp-then-rename, delete-then-recreate,
// multi-fsync) without feeling laggy.
const DefaultDebounce = 75 * time.Millisecond

// Option configures a [Source] returned by [New].
type Option func(*options)

type options struct {
	parseOptions []dotenv.ParseOption
	debounce     time.Duration
	logger       *slog.Logger
}

func defaultOptions() options {
	return options{
		debounce: DefaultDebounce,
		logger:   slog.New(slog.DiscardHandler),
	}
}

// WithParseOptions forwards opts to every parse the watcher performs
// (initial load and each reload).
func WithParseOptions(opts ...dotenv.ParseOption) Option {
	return func(o *options) {
		o.parseOptions = append(o.parseOptions, opts...)
	}
}

// WithDebounce overrides the quiet period before reload. Negative
// values are clamped to 0 (reload on every event).
func WithDebounce(d time.Duration) Option {
	return func(o *options) {
		if d < 0 {
			d = 0
		}
		o.debounce = d
	}
}

// WithLogger directs non-fatal diagnostics (reload failures, fsnotify
// errors, dropped events) to l. Default is a discard logger.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}
