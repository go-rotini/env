package env

import (
	"fmt"
	"log/slog"
	"reflect"
	"time"
)

// Option configures a [Loader]. Apply via [New] or [LoadTo].
type Option func(*loaderOptions)

type loaderOptions struct {
	sources        []Source
	prefix         string
	tagName        string
	fieldNameXform func(string) string
	requireAll     bool
	logger         *slog.Logger
	customDecoders map[reflect.Type]customDecoderFn
	mutators       []Mutator
	secretRedactor func(string) string
	withoutUnset   bool
	pollInterval   time.Duration
	reloadDebounce time.Duration
}

type customDecoderFn func(s string, dst reflect.Value) error

func defaultLoaderOptions() loaderOptions {
	return loaderOptions{
		sources:        []Source{OSEnv()},
		tagName:        "env",
		fieldNameXform: toScreamingSnake,
		secretRedactor: defaultSecretRedactor,
		reloadDebounce: DefaultReloadDebounce,
	}
}

// DefaultReloadDebounce is the quiet period [Loader.Watch] and [Live]
// wait after the last [Source]-change event before reloading. 75ms
// covers an editor save's burst of fsnotify events without feeling
// laggy in interactive use.
const DefaultReloadDebounce = 75 * time.Millisecond

// defaultSecretRedactor renders a length-revealing marker:
//
//	"" → "<redacted>"
//	"x" → "<redacted: 1 byte>"
//	"abcdef" → "<redacted: 6 bytes>"
func defaultSecretRedactor(value string) string {
	if value == "" {
		return secretMarker
	}
	suffix := "bytes"
	if len(value) == 1 {
		suffix = "byte"
	}
	return fmt.Sprintf("<redacted: %d %s>", len(value), suffix)
}

// WithSource replaces the loader's source chain. The default chain is
// [OSEnv] alone; to layer additional sources, list them explicitly:
//
//	env.WithSource(env.OSEnv(), dotenvSrc)
//
// Sources are consulted in order; the first to return ok=true wins. An
// empty argument list produces a loader whose chain has no entries.
func WithSource(srcs ...Source) Option {
	return func(o *loaderOptions) {
		dup := make([]Source, len(srcs))
		copy(dup, srcs)
		o.sources = dup
	}
}

// WithSourceAppend adds src to the end of the existing source chain
// (lowest precedence — consulted only when no earlier source matches).
func WithSourceAppend(src Source) Option {
	return func(o *loaderOptions) {
		o.sources = append(o.sources, src)
	}
}

// WithSourcePrepend adds src to the front of the existing source chain
// (highest precedence).
func WithSourcePrepend(src Source) Option {
	return func(o *loaderOptions) {
		o.sources = append([]Source{src}, o.sources...)
	}
}

// WithPrefix prepends prefix to every resolved env-var name, ahead of
// any envPrefix accumulated from nested struct fields. It does not apply
// to `expand` references.
func WithPrefix(prefix string) Option {
	return func(o *loaderOptions) {
		o.prefix = prefix
	}
}

// WithTagName overrides the struct tag name the loader recognizes
// (default "env"), for structs already tagged under another name. The
// tag grammar stays the same. Does not affect the envDesc / envPrefix
// companion tags.
//
// Loaders with a non-default tag name bypass the per-type field-info
// cache and re-walk the struct on every Load.
func WithTagName(name string) Option {
	return func(o *loaderOptions) {
		o.tagName = name
	}
}

// WithFieldNameTransform overrides the default SCREAMING_SNAKE_CASE
// transform for fields without an explicit tag name. fn receives the
// Go field name and returns the env-var name.
//
// Loaders with a custom transform bypass the per-type field-info cache
// and re-walk the struct on every Load.
func WithFieldNameTransform(fn func(goFieldName string) string) Option {
	return func(o *loaderOptions) {
		if fn != nil {
			o.fieldNameXform = fn
		}
	}
}

// WithRequireAll inverts the default optional-by-default policy: every
// field becomes required unless it has a default= tag option.
// [FieldDoc.Required] does not reflect it.
func WithRequireAll() Option {
	return func(o *loaderOptions) {
		o.requireAll = true
	}
}

// WithLogger directs non-fatal loader diagnostics to l. Default is a
// no-op logger.
func WithLogger(l *slog.Logger) Option {
	return func(o *loaderOptions) {
		o.logger = l
	}
}

// WithMutator registers one or more [Mutator] values in chain order. May be
// called multiple times to extend the chain. The chain runs once per
// field, after source lookup and default application, before decoding.
func WithMutator(m ...Mutator) Option {
	return func(o *loaderOptions) {
		o.mutators = append(o.mutators, m...)
	}
}

// WithSecretRedactor overrides the redaction function applied to
// secret-tagged field values in [*ParseError] and
// [*ImmutableChangedError]. The default returns "<redacted: N bytes>"
// (or "<redacted>" for empty). Pass nil to restore the default. The
// encoder's separate "***" marker is unaffected.
func WithSecretRedactor(fn func(value string) string) Option {
	return func(o *loaderOptions) {
		if fn == nil {
			o.secretRedactor = defaultSecretRedactor
			return
		}
		o.secretRedactor = fn
	}
}

// WithoutUnset disables the per-field `unset` tag option globally,
// keeping the process env inspectable after Load. Useful in tests.
func WithoutUnset() Option {
	return func(o *loaderOptions) {
		o.withoutUnset = true
	}
}

// WithPollInterval configures how often [Loader.Watch] and [Live]
// re-read sources that do not implement [Watcher]. Default 0 (no
// polling); a positive duration enables poll-based reload for
// non-Watcher sources. Watcher sources push events regardless.
// Negative values are clamped to 0.
func WithPollInterval(d time.Duration) Option {
	return func(o *loaderOptions) {
		if d < 0 {
			d = 0
		}
		o.pollInterval = d
	}
}

// WithReloadDebounce coalesces burst events from [Watcher] sources:
// the worker waits for d of quiet before re-running the load pipeline;
// events arriving within d collapse into one reload. Default
// [DefaultReloadDebounce]. Negative values are clamped to 0 (no
// debouncing — intended for tests only).
func WithReloadDebounce(d time.Duration) Option {
	return func(o *loaderOptions) {
		if d < 0 {
			d = 0
		}
		o.reloadDebounce = d
	}
}

// WithCustomDecoder registers a custom decoder for type T. Runs at the
// top of the decoder priority chain, ahead of [Unmarshaler],
// [encoding.TextUnmarshaler], [encoding.BinaryUnmarshaler], and the
// built-in decoders. fn receives the resolved string and a pointer to
// the destination of type T. It applies to fields of exactly type T,
// not to slice elements or map keys and values.
func WithCustomDecoder[T any](fn func(s string, dst *T) error) Option {
	return func(o *loaderOptions) {
		if o.customDecoders == nil {
			o.customDecoders = make(map[reflect.Type]customDecoderFn)
		}
		typ := reflect.TypeFor[T]()
		o.customDecoders[typ] = func(s string, dst reflect.Value) error {
			ptr, _ := reflect.TypeAssert[*T](dst.Addr())
			return fn(s, ptr)
		}
	}
}
