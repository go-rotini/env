package env

import "sync"

// Lazy package-level loader for [Load] / [MustLoad] / [LoadTo] / [MustLoadTo],
// keeping package init side-effect free.
var (
	defaultLoaderOnce sync.Once
	defaultLoaderInst *Loader
)

func resolveDefaultLoader() *Loader {
	defaultLoaderOnce.Do(func() {
		defaultLoaderInst = New()
	})
	return defaultLoaderInst
}

// Load decodes environment variables into v using the default [Loader]
// (OS env source, no prefix, "env" tag name). v must be a non-nil pointer
// to a struct. Per-field errors are aggregated into a [*MultiError];
// field-collection errors (conflicting names, unknown tag options) are
// returned directly.
func Load(v any) error {
	return resolveDefaultLoader().Load(v)
}

// MustLoad is like [Load] but panics on error.
func MustLoad(v any) {
	if err := Load(v); err != nil {
		panic(err)
	}
}

// LoadTo is the generic form of [Load]. T must be a struct type. opts
// are applied to a fresh [Loader]; with no opts the default loader is
// used.
func LoadTo[T any](opts ...Option) (T, error) {
	var v T
	loader := resolveDefaultLoader()
	if len(opts) > 0 {
		loader = New(opts...)
	}
	if err := loader.Load(&v); err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// MustLoadTo is like [LoadTo] but panics on error.
func MustLoadTo[T any](opts ...Option) T {
	v, err := LoadTo[T](opts...)
	if err != nil {
		panic(err)
	}
	return v
}
