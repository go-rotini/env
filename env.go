package env

import "sync"

// defaultLoader is the package-level [Loader] used by [Load], [MustLoad],
// [LoadTo], and [MustLoadTo]. It is constructed lazily on first use to
// keep the package init side-effect free.
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

// Load decodes environment variables into v using the package-level
// default [Loader] (OS env source, no prefix, "env" tag name).
//
// v must be a non-nil pointer to a struct. Per-field errors are
// aggregated; field-collection errors (conflicting names, unknown tag
// options) are returned directly. Use [errors.Is] to test against
// [ErrMissingRequired], [ErrParse], etc.
func Load(v any) error {
	return resolveDefaultLoader().Load(v)
}

// MustLoad is like [Load] but panics on error. Intended for use in
// main() where a misconfigured environment is unrecoverable.
func MustLoad(v any) {
	if err := Load(v); err != nil {
		panic(err)
	}
}

// LoadTo is the generic, return-the-value form of [Load]. T must be a
// struct type. opts are applied to a fresh [Loader] (the default loader
// is used when opts is empty).
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
