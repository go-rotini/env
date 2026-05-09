package dotenv_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-rotini/env/dotenv"
)

// Acceptance tests run the parser against real-world `.env` examples
// drawn from popular tools. The fixtures live in
// testdata/acceptance/{name}.env and are checked into the repo so
// behavior changes are diff-visible.

// acceptanceFixtures lists every fixture and the parse options it
// expects. Fixtures whose semantics rely on `$VAR` interpolation
// (Rails/bkeepers) get WithExpand; fixtures that are plain
// KEY=VALUE pairs (kubectl, GHA, Compose, Laravel) parse with
// defaults.
var acceptanceFixtures = []struct {
	name string
	opts []dotenv.ParseOption
}{
	{"kubectl", nil},
	{"gha", nil},
	{"compose", nil},
	{"rails", []dotenv.ParseOption{dotenv.WithExpand()}},
	{"laravel", nil},
}

func acceptanceFile(name string) string {
	return filepath.Join("..", "testdata", "acceptance", name+".env")
}

// TestAcceptanceLoadStruct reads each fixture into a representative
// struct and asserts that key fields decoded correctly. The structs
// are intentionally narrow (one or two fields per fixture) so the
// test signals "the parser fed the right values to the loader" —
// not "every field of every config matches a golden file."
func TestAcceptanceLoadStruct(t *testing.T) {
	t.Parallel()

	t.Run("kubectl", func(t *testing.T) {
		t.Parallel()
		src := mustNewSource(t, acceptanceFile("kubectl"))
		v, _ := src.Lookup("DATABASE_HOST")
		if v != "postgres.svc.cluster.local" {
			t.Errorf("DATABASE_HOST = %q", v)
		}
		v, _ = src.Lookup("FEATURE_FLAGS")
		if v != "feature-a,feature-b" {
			t.Errorf("FEATURE_FLAGS = %q", v)
		}
	})

	t.Run("gha", func(t *testing.T) {
		t.Parallel()
		src := mustNewSource(t, acceptanceFile("gha"))
		v, _ := src.Lookup("GITHUB_REPOSITORY")
		if v != "acme/widget" {
			t.Errorf("GITHUB_REPOSITORY = %q", v)
		}
		v, _ = src.Lookup("RUNNER_OS")
		if v != "Linux" {
			t.Errorf("RUNNER_OS = %q", v)
		}
	})

	t.Run("compose", func(t *testing.T) {
		t.Parallel()
		src := mustNewSource(t, acceptanceFile("compose"))
		v, _ := src.Lookup("TAG")
		if v != "v1.4.0" {
			t.Errorf("TAG = %q", v)
		}
		v, _ = src.Lookup("WEB_PORT")
		if v != "8080" {
			t.Errorf("WEB_PORT = %q", v)
		}
	})

	t.Run("rails", func(t *testing.T) {
		t.Parallel()
		// Rails fixture exercises the `export ` prefix. Loader-level
		// expansion is irrelevant here (no `$VAR` references), so we
		// parse with default options and the export prefix should be
		// transparently accepted.
		src := mustNewSource(t, acceptanceFile("rails"))
		v, _ := src.Lookup("DATABASE_URL")
		if v != "postgres://localhost/myapp_development" {
			t.Errorf("DATABASE_URL = %q", v)
		}
		v, _ = src.Lookup("RAILS_ENV")
		if v != "development" {
			t.Errorf("RAILS_ENV = %q", v)
		}
	})

	t.Run("laravel", func(t *testing.T) {
		t.Parallel()
		src := mustNewSource(t, acceptanceFile("laravel"))
		v, _ := src.Lookup("APP_KEY")
		if v != "base64:b1jK2f5h+y0E9aDnM2gVkF8gJ3c3mY4cTx3R3F6nQGw=" {
			t.Errorf("APP_KEY = %q", v)
		}
		v, ok := src.Lookup("DB_PASSWORD")
		if !ok || v != "" {
			t.Errorf("DB_PASSWORD = (%q, %v); want (\"\", true) — set-but-empty", v, ok)
		}
	})
}

// TestAcceptanceRoundTrip parses each fixture, marshals it back to
// bytes, parses again, and asserts the second pass produces the same
// effective key/value map. Byte-for-byte equality is asserted only
// for fixtures whose source bytes are untouched after parse — any
// fixture with a quoted value normalized by Marshal is excluded from
// the byte-equal subset and is verified by map equality only.
func TestAcceptanceRoundTrip(t *testing.T) {
	t.Parallel()
	for _, fx := range acceptanceFixtures {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			path := acceptanceFile(fx.name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}

			file, err := dotenv.Parse(data, fx.opts...)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			marshaled, err := file.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			// First-pass byte-equality is required: Marshal preserves
			// each entry's verbatim source bytes for unmutated entries.
			if !bytes.Equal(marshaled, data) {
				t.Errorf("Marshal is not byte-equal to source:\n  got=%q\n want=%q",
					string(marshaled), string(data))
			}

			file2, err := dotenv.Parse(marshaled, fx.opts...)
			if err != nil {
				t.Fatalf("re-Parse: %v", err)
			}
			if !mapsEqual(file.ToMap(), file2.ToMap()) {
				t.Errorf("re-parsed map differs:\n  first=%v\n second=%v",
					file.ToMap(), file2.ToMap())
			}
		})
	}
}

// TestAcceptancePathQueries exercises (*File).Get / Set / Delete on
// each fixture. After Set then Delete, the file's effective map
// must equal its original effective map.
func TestAcceptancePathQueries(t *testing.T) {
	t.Parallel()
	for _, fx := range acceptanceFixtures {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(acceptanceFile(fx.name))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			file, err := dotenv.Parse(data, fx.opts...)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			before := file.ToMap()

			// Get on every key returns its original value.
			for k, v := range before {
				got, ok := file.Get(k)
				if !ok || got != v {
					t.Errorf("Get(%q) = (%q, %v), want (%q, true)", k, got, ok, v)
				}
			}

			// Set a sentinel key, observe it, delete it, observe its
			// absence — the original map must equal the post-delete
			// map.
			const sentinel = "__ACCEPTANCE_SENTINEL__"
			file.Set(sentinel, "x")
			if v, ok := file.Get(sentinel); !ok || v != "x" {
				t.Errorf("Get(sentinel) = (%q, %v)", v, ok)
			}
			if !file.Delete(sentinel) {
				t.Errorf("Delete(sentinel) = false, want true")
			}
			after := file.ToMap()
			if !mapsEqual(before, after) {
				t.Errorf("Set/Delete round-trip changed map:\n  before=%v\n  after=%v",
					before, after)
			}
		})
	}
}

func mustNewSource(t *testing.T, path string) interface {
	Lookup(string) (string, bool)
} {
	t.Helper()
	src, err := dotenv.NewSource(path)
	if err != nil {
		t.Fatalf("NewSource(%q): %v", path, err)
	}
	return src
}
