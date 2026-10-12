package env

import (
	"context"
	"maps"
	"os"
	"strings"
)

// Source provides string-keyed value lookups. It is the primary contract by
// which the loader resolves env-var values: the OS environment is just one
// source, and `.env` files, in-memory maps, and custom backends all
// implement the same interface.
//
// Implementations MUST make Lookup safe for concurrent use. All built-in
// sources satisfy this; third-party sources should as well.
type Source interface {
	// Lookup returns the value associated with key and whether the key is
	// set. ok=true with an empty value distinguishes "set but empty" from
	// "unset"; this distinction is preserved end-to-end through the loader.
	Lookup(key string) (value string, ok bool)
}

// Watcher is optionally implemented by sources that can push change
// notifications. Sources that don't implement Watcher are polled at the
// interval set by [WithPollInterval] when used under [Loader.Watch] or
// [Live]; without that option, non-Watcher sources are read once at the
// initial load and never re-read.
type Watcher interface {
	// Watch returns a channel that delivers Change events as the source's
	// underlying state changes. The returned channel MUST be closed when
	// ctx is canceled or when the source determines it can no longer
	// produce events (for example, the underlying file is deleted with
	// no re-bind possible). Each call to Watch returns an independent
	// subscription.
	Watch(ctx context.Context) (<-chan Change, error)
}

// Change describes a Source-level change. Keys may list the variable names
// that were added, modified, or removed since the last event, or be empty
// when the source cannot tell. The loader does not use Keys: every reload
// re-reads all fields.
type Change struct {
	Keys []string
}

// OSEnv returns a Source backed by [os.LookupEnv]. The source is
// stateless and reflects the live process environment on every Lookup.
//
// On Windows, [os.LookupEnv] matches variable names case-insensitively;
// on POSIX systems names are case-sensitive.
//
// Lookup rejects degenerate keys (empty, leading "=", containing "="
// or NUL) without consulting the OS, so callers passing such keys
// always observe ok=false instead of triggering platform-specific edge
// cases like Windows "=C:" per-drive hidden variables.
func OSEnv() Source { return osEnvSource }

var osEnvSource Source = osEnvType{}

type osEnvType struct{}

func (osEnvType) Lookup(key string) (string, bool) {
	if key == "" || key[0] == '=' || strings.ContainsAny(key, "=\x00") {
		return "", false
	}
	return os.LookupEnv(key)
}

// MapOption configures a [Map] source.
type MapOption func(*mapOptions)

type mapOptions struct {
	caseInsensitive bool
}

// WithCaseInsensitive returns a [MapOption] that makes the [Map] source
// fold ASCII keys to uppercase for comparison, approximating Windows
// env-var semantics. Useful in cross-platform tests that need to exercise
// case-insensitive lookup on a POSIX host.
//
// Folding is ASCII-only; non-ASCII bytes pass through unchanged.
func WithCaseInsensitive() MapOption {
	return func(o *mapOptions) { o.caseInsensitive = true }
}

// Map returns a Source backed by a copy of m. Mutating m after construction
// does not affect the Source. The returned Source is safe for concurrent
// Lookup.
//
// Pass [WithCaseInsensitive] to enable Windows-style ASCII case-insensitive
// key matching.
func Map(m map[string]string, opts ...MapOption) Source {
	cfg := mapOptions{}
	for _, opt := range opts {
		opt(&cfg)
	}

	dup := make(map[string]string, len(m))
	if cfg.caseInsensitive {
		for k, v := range m {
			dup[asciiUpper(k)] = v
		}
	} else {
		maps.Copy(dup, m)
	}

	return &mapSource{m: dup, caseInsensitive: cfg.caseInsensitive}
}

type mapSource struct {
	m               map[string]string
	caseInsensitive bool
}

func (s *mapSource) Lookup(key string) (string, bool) {
	if s.caseInsensitive {
		key = asciiUpper(key)
	}
	v, ok := s.m[key]
	return v, ok
}

// Multi returns a Source that consults srcs in order; the first source
// returning ok=true wins. An empty src list returns a Source whose Lookup
// always returns ("", false). A single-source argument is returned as-is
// without an additional indirection.
func Multi(srcs ...Source) Source {
	switch len(srcs) {
	case 0:
		return emptySource{}
	case 1:
		return srcs[0]
	}
	cp := make([]Source, len(srcs))
	copy(cp, srcs)
	return &multiSource{srcs: cp}
}

type multiSource struct {
	srcs []Source
}

func (m *multiSource) Lookup(key string) (string, bool) {
	for _, s := range m.srcs {
		if v, ok := s.Lookup(key); ok {
			return v, ok
		}
	}
	return "", false
}

type emptySource struct{}

func (emptySource) Lookup(string) (string, bool) { return "", false }

// asciiUpper folds ASCII lowercase to uppercase and leaves non-ASCII
// bytes unchanged, matching the runtime's Windows env-var case folding.
func asciiUpper(s string) string {
	needFold := false
	for i := range len(s) {
		if s[i] >= 'a' && s[i] <= 'z' {
			needFold = true
			break
		}
	}
	if !needFold {
		return s
	}
	b := make([]byte, len(s))
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		b[i] = c
	}
	return string(b)
}
