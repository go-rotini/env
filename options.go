package env

import (
	"fmt"
	"log/slog"
	"reflect"
	"time"
)

// Option configures a [Loader]. Apply via [New] or via [LoadTo].
type Option func(*loaderOptions)

// loaderOptions holds the resolved configuration for a Loader. Fields are
// unexported; Option functions are the only way to set them.
type loaderOptions struct {
	sources        []Source
	prefix         string
	tagName        string
	fieldNameXform func(string) string
	relaxedNames   bool
	requireAll     bool
	logger         *slog.Logger
	customDecoders map[reflect.Type]customDecoderFn
	mutators       []Mutator
	secretRedactor func(string) string
	withoutUnset   bool
	pollInterval   time.Duration
	reloadDebounce time.Duration
}

// customDecoderFn is the dispatcher signature for decoders registered via
// [WithCustomDecoder]. It receives the resolved string value and an
// addressable [reflect.Value] of the registered type.
type customDecoderFn func(s string, dst reflect.Value) error

// defaultLoaderOptions returns the zero-config defaults.
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
// wait after the last [Source]-change event before performing a reload.
// 75ms covers every common burst pattern (an editor save sequence
// produces fsnotify events for write/chmod/close in quick succession)
// without feeling laggy in interactive use.
const DefaultReloadDebounce = 75 * time.Millisecond

// defaultSecretRedactor renders a length-revealing marker for secret
// values that surface in error messages or [Describe] output.
//
//	"" => "<redacted>"   (no length leak; keeps logs grep-friendly)
//	"x" => "<redacted: 1 byte>"
//	"abcdef" => "<redacted: 6 bytes>"
//
// Override via [WithSecretRedactor].
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

// WithSource REPLACES the loader's source chain. The default loader uses
// only [OSEnv]. To layer additional sources behind OS env, list them
// explicitly: env.WithSource(env.OSEnv(), dotenvSrc). Sources are
// consulted in order; the first to return ok=true wins.
//
// An empty argument list is valid and produces a loader whose source
// chain has no entries (lookups always return ("", false)).
func WithSource(srcs ...Source) Option {
	return func(o *loaderOptions) {
		dup := make([]Source, len(srcs))
		copy(dup, srcs)
		o.sources = dup
	}
}

// WithSourceAppend adds src to the END of the existing source chain. The
// appended source has the LOWEST precedence (consulted only when no
// earlier source returns ok=true).
func WithSourceAppend(src Source) Option {
	return func(o *loaderOptions) {
		o.sources = append(o.sources, src)
	}
}

// WithSourcePrepend adds src to the FRONT of the existing source chain.
// The prepended source has the HIGHEST precedence.
func WithSourcePrepend(src Source) Option {
	return func(o *loaderOptions) {
		o.sources = append([]Source{src}, o.sources...)
	}
}

// WithPrefix prepends prefix to every resolved env-var name. Applied
// before any tag-supplied envPrefix on nested structs.
func WithPrefix(prefix string) Option {
	return func(o *loaderOptions) {
		o.prefix = prefix
	}
}

// WithTagName overrides the struct tag name the loader recognizes.
// Default is "env". Set to "envconfig" for caarlos0/envconfig-style
// migrations. Does not affect the separate envDesc / envPrefix companion
// tags, which always use those names.
//
// Loaders configured with a non-default tag name bypass the per-type
// field-info cache and re-walk the struct on every Load.
func WithTagName(name string) Option {
	return func(o *loaderOptions) {
		o.tagName = name
	}
}

// WithFieldNameTransform overrides the default SCREAMING_SNAKE_CASE
// transform applied to fields without an explicit tag name. The function
// receives the exact Go field name and returns the env-var name.
//
// Loaders configured with a custom transform bypass the per-type
// field-info cache and re-walk the struct on every Load.
func WithFieldNameTransform(fn func(goFieldName string) string) Option {
	return func(o *loaderOptions) {
		if fn != nil {
			o.fieldNameXform = fn
		}
	}
}

// WithRelaxedNames accepts identifiers containing characters outside the
// POSIX [A-Za-z_][A-Za-z0-9_]* class (hyphens, dots, non-ASCII). Default
// is POSIX-strict.
//
// In Phase 5 the option is recorded but not yet consulted; subordinate
// sources (the dotenv parser added in Phase 9) read it to relax their
// identifier validation. The OSEnv source has no notion of "validity"
// beyond what the OS itself accepts and is unaffected.
func WithRelaxedNames() Option {
	return func(o *loaderOptions) {
		o.relaxedNames = true
	}
}

// WithRequireAll inverts the default optional-by-default policy. With
// WithRequireAll set, every untagged field becomes implicitly required
// unless it has a default= tag option. Useful for paranoid configs where
// forgetting a value should fail closed.
func WithRequireAll() Option {
	return func(o *loaderOptions) {
		o.requireAll = true
	}
}

// WithLogger directs non-fatal diagnostics emitted by the loader to l.
// Default is a no-op logger; the option is recorded for use by the
// Mutator chain (Phase 7) and the Watcher poll loop (Phase 11).
func WithLogger(l *slog.Logger) Option {
	return func(o *loaderOptions) {
		o.logger = l
	}
}

// WithMutator registers one or more [Mutator]s in chain order. May be
// called multiple times to extend the chain.
//
// The chain runs once per field, after source lookup and default
// application but before decoding. See [Mutator] for the full contract.
func WithMutator(m ...Mutator) Option {
	return func(o *loaderOptions) {
		o.mutators = append(o.mutators, m...)
	}
}

// WithSecretRedactor overrides the redaction function applied to
// secret-tagged field values that surface in error messages and
// [Describe] / [PrintUsage] / [Markdown] output.
//
// The default redactor returns "<redacted: N bytes>" (or "<redacted>"
// for empty values). The encoder's secret-tag handling (see Phase 6)
// uses a separate hardcoded "***" marker and is unaffected by this
// option.
//
// Setting fn to nil restores the default redactor.
func WithSecretRedactor(fn func(value string) string) Option {
	return func(o *loaderOptions) {
		if fn == nil {
			o.secretRedactor = defaultSecretRedactor
			return
		}
		o.secretRedactor = fn
	}
}

// WithoutUnset disables the per-field `unset` tag option globally for
// this loader. Useful in test environments where the process env should
// remain inspectable after Load.
//
// The default behavior (without this option) is that any field tagged
// with `unset` triggers an [os.Unsetenv] call after a successful decode.
func WithoutUnset() Option {
	return func(o *loaderOptions) {
		o.withoutUnset = true
	}
}

// WithPollInterval configures how often [Loader.Watch] and [Live]
// re-read sources that do NOT implement [Watcher]. Default 0, which
// means non-Watcher sources are read once at the initial load and
// never re-read; pass a positive duration to enable poll-based reload
// for those sources. Watcher-implementing sources push events
// regardless of this setting.
//
// Negative durations are clamped to zero (polling disabled).
func WithPollInterval(d time.Duration) Option {
	return func(o *loaderOptions) {
		if d < 0 {
			d = 0
		}
		o.pollInterval = d
	}
}

// WithReloadDebounce coalesces burst events from [Watcher]-implementing
// sources. The watcher waits for `d` of quiet after the last incoming
// change event before re-running the load pipeline; multiple events
// within `d` collapse into one reload. Default
// [DefaultReloadDebounce] (75ms).
//
// Negative durations are clamped to zero (no debouncing — every change
// event triggers an immediate reload, intended for tests only).
func WithReloadDebounce(d time.Duration) Option {
	return func(o *loaderOptions) {
		if d < 0 {
			d = 0
		}
		o.reloadDebounce = d
	}
}

// WithCustomDecoder registers a custom decoder for type T. It runs at the
// top of the decoder priority chain, ahead of [Unmarshaler],
// [encoding.TextUnmarshaler], [encoding.BinaryUnmarshaler], and the
// built-in registry. Useful for overriding decoding for types that
// already implement one of those interfaces.
//
// The function receives the resolved string value and a pointer to the
// destination of type T; it should populate *T and return nil on
// success.
func WithCustomDecoder[T any](fn func(s string, dst *T) error) Option {
	return func(o *loaderOptions) {
		if o.customDecoders == nil {
			o.customDecoders = make(map[reflect.Type]customDecoderFn)
		}
		typ := reflect.TypeFor[T]()
		o.customDecoders[typ] = func(s string, dst reflect.Value) error {
			// The loader looks up the decoder by exact type, so the
			// destination is always *T; the type assertion is forced.
			ptr, _ := dst.Addr().Interface().(*T)
			return fn(s, ptr)
		}
	}
}
