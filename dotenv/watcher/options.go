package watcher

import (
	"log/slog"
	"time"

	"github.com/go-rotini/env/dotenv"
)

// DefaultDebounce is the default quiet period the watcher waits for after
// the last fsnotify event before re-parsing the target file. 75ms covers
// the burst patterns produced by every common editor save sequence
// (write-temp-then-rename, delete-then-recreate, multi-fsync) without
// being long enough to feel laggy in interactive use.
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

// WithParseOptions forwards the supplied [dotenv.ParseOption] values to
// every parse the watcher performs (initial load and each post-change
// re-parse). Use it when the target file relies on `.env`-parser features
// like [dotenv.WithExpand] or [dotenv.WithBackticks].
func WithParseOptions(opts ...dotenv.ParseOption) Option {
	return func(o *options) {
		o.parseOptions = append(o.parseOptions, opts...)
	}
}

// WithDebounce overrides the quiet-period delay between the last
// fsnotify event and the re-parse. Negative values are clamped to zero
// (re-parse on every event); zero disables debouncing entirely.
func WithDebounce(d time.Duration) Option {
	return func(o *options) {
		if d < 0 {
			d = 0
		}
		o.debounce = d
	}
}

// WithLogger directs the watcher's non-fatal diagnostics — re-parse
// failures, fsnotify error-channel messages, dropped events on a slow
// subscriber — to l. The default is a discard logger; nothing is logged
// when WithLogger is not set.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}
