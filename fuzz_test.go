package env_test

import (
	"net"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/go-rotini/env"
)

// Fuzz targets at the loader / decoder layer. The contract is:
//
//	1. No panic for any input.
//	2. No infinite loop (Go's fuzz framework times out after 10s
//	   per execution, but invariants below catch loops earlier).
//	3. Errors are fine — Load returning an error on bad input is
//	   the expected, well-behaved outcome.
//
// Seed corpora live in testdata/fuzz/{TestName}/.

// FuzzLoader exercises the loader pipeline with arbitrary key/value
// bytes against a struct that covers the four most common decode
// targets (string, int, bool, []string). Catches panics in field
// collection, source lookup, and the decoder.
func FuzzLoader(f *testing.F) {
	for _, p := range []struct {
		key, value string
	}{
		{"FOO", "bar"},
		{"PORT", "8080"},
		{"DEBUG", "true"},
		{"TAGS", "a,b,c"},
		{"NEG", "-1"},
		{"BAD", "not-a-number"},
		{"EMPTY", ""},
		{"WS", "   "},
		{"UNICODE", "héllo-世界"},
		{"NUL", "ab\x00cd"},
	} {
		f.Add(p.key, p.value)
	}

	type fuzzCfg struct {
		S string   `env:"FOO"`
		I int      `env:"PORT"`
		B bool     `env:"DEBUG"`
		L []string `env:"TAGS"`
	}

	f.Fuzz(func(t *testing.T, key, value string) {
		loader := env.New(env.WithSource(env.Map(map[string]string{key: value})))
		var cfg fuzzCfg
		// Errors are fine; the contract is no panic.
		_ = loader.Load(&cfg)

		// Snapshot is also expected to run without panic for any
		// config-shaped input.
		_, _ = loader.Snapshot(&cfg)
	})
}

// FuzzDecode targets the value-decoder layer. For every fuzzed input
// string, run the loader against a battery of single-field structs
// covering the major built-in types. Any panic indicates a decoder
// bug for that type.
func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		"", " ", "0", "1", "-1", "42", "9999999999999999999999",
		"3.14", "1e308", "NaN", "+Inf",
		"true", "false", "TrUe", "yes", "no", "on", "off", "1", "0",
		"1h30m", "0", "100ms", "-5s",
		"2024-01-01T00:00:00Z", "2024-12-31",
		"https://example.com/path?q=1", "ftp://[invalid",
		"10.0.0.0/8", "::1/128",
		"^[a-z]+$", "(unclosed",
		"a,b,c", ",,,",
		"a:1,b:2", "k=v",
		"\xff\xfe\xfd", "BOM\xef\xbb\xbf",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, value string) {
		src := env.Map(map[string]string{"X": value})
		loader := env.New(env.WithSource(src))

		// Run the same value through each decoder. Any panic in the
		// type-specific decoder would surface here. Errors are fine.
		targets := []any{
			new(struct {
				X string `env:"X"`
			}),
			new(struct {
				X int `env:"X"`
			}),
			new(struct {
				X int64 `env:"X"`
			}),
			new(struct {
				X uint64 `env:"X"`
			}),
			new(struct {
				X float64 `env:"X"`
			}),
			new(struct {
				X bool `env:"X"`
			}),
			new(struct {
				X time.Duration `env:"X"`
			}),
			new(struct {
				X time.Time `env:"X"`
			}),
			new(struct {
				X *url.URL `env:"X"`
			}),
			new(struct {
				X net.IP `env:"X"`
			}),
			new(struct {
				X *regexp.Regexp `env:"X"`
			}),
			new(struct {
				X []string `env:"X"`
			}),
			new(struct {
				X []int `env:"X"`
			}),
			new(struct {
				X map[string]string `env:"X"`
			}),
		}
		for _, target := range targets {
			_ = loader.Load(target)
		}
	})
}
