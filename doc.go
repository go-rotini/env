// Package env loads environment variables (and optional .env files) into
// Go structs, with first-class support for live reload.
//
// The mental model is [encoding/json]: a struct gets populated from a set of
// named values via tags, with extension points for custom types, sources, and
// lifecycle hooks. The goal is the smallest possible top-level surface for
// the 80% case ([Load]) and a principled extension story for everything else.
//
// # One-line happy path
//
//	type Config struct {
//	    Port     int           `env:"PORT,default=8080"`
//	    DBURL    string        `env:"DATABASE_URL,required"`
//	    Timeout  time.Duration `env:"TIMEOUT,default=30s"`
//	    Hosts    []string      `env:"HOSTS,separator=;"`
//	    APIKey   string        `env:"API_KEY,required,secret"`
//	}
//
//	var cfg Config
//	if err := env.Load(&cfg); err != nil {
//	    log.Fatal(env.FormatError(err))
//	}
//
// # Sources
//
// All lookups go through the [Source] interface. The OS environment is just
// one source; .env files (via the env/dotenv sub-package), in-memory maps,
// and remote secret backends (in their own modules) are equal citizens.
//
//	loader := env.New(
//	    env.WithSource(env.OSEnv()),
//	    env.WithPrefix("APP_"),
//	)
//
// # Live reload
//
// [NewLive] wraps the loader in a typed handle that swaps the configuration
// atomically when an underlying [Source] reports a change. Reads are O(1)
// and lock-free via [sync/atomic.Pointer]; readers always observe a complete,
// validated snapshot.
//
//	live, err := env.NewLive[Config](env.WithSource(env.OSEnv()))
//	if err != nil { log.Fatal(err) }
//	defer live.Close()
//
//	cfg := live.Get()  // *Config — never nil after NewLive succeeds
//
// # Sub-packages
//
//   - env/dotenv          — .env file parser, AST, lossless round-trip, [Lint]
//   - env/dotenv/watcher  — fsnotify-backed file watching with atomic-rename support
//   - env/envtest         — Snapshot/Restore helpers for tests that mutate os.Environ
//
// The root package has no third-party runtime dependencies. Sub-packages add
// dependencies (fsnotify in env/dotenv/watcher) only when their features are
// imported.
//
// # Tag system
//
// A single comma-separated tag, mirroring [encoding/json]:
//
//	`env:"NAME,opt1,opt2"`
//
// Supported options: required, notEmpty, default=value, separator=char,
// kvSeparator=char, expand, fromFile, secret, immutable, layout=format,
// base64, hex, unset. Companion tags: envDesc:"..." for free-text help and
// envPrefix:"PFX_" on nested struct fields. See the package README and the
// requirements document for the full table.
//
// # Pitfalls
//
// Environment variables come with platform-specific quirks. The list below is
// the curated subset most likely to bite a new user; the full catalog lives
// in §12 of the package's design document.
//
// OS-level:
//
//   - Windows folds env-var names to uppercase; POSIX is case-sensitive. The
//     [OSEnv] source matches the platform — for cross-platform tests, use
//     [Map] with [WithCaseInsensitive].
//   - [os.Getenv] collapses unset and empty into "". The [Source] interface
//     preserves the distinction via the second bool return; [OSEnv] uses
//     [os.LookupEnv] to honor it. The `required` tag option is satisfied by
//     a set-but-empty value; `notEmpty` requires both set and non-empty.
//   - [os.Setenv] / [os.Unsetenv] are not thread-safe under cgo (golang/go
//     #63567). This package never calls them from library code; the `unset`
//     tag option is the single opt-in exception, serialized within a Load
//     call. Disable globally via [WithoutUnset] in test environments.
//   - [os.Environ] returns degenerate entries on Windows (empty keys,
//     leading-`=` per-drive variables). [OSEnv] filters them; you don't see
//     them via [Source.Lookup].
//
// Dotenv parsing:
//
//   - CRLF / bare CR line endings are normalized; the parser produces the
//     same AST regardless of which line ending the file used.
//   - UTF-8 BOM at offset 0 is stripped; a BOM elsewhere is treated as a
//     literal character.
//   - Comments inside quoted values are part of the value; the cheap
//     "split on #" approach is wrong. Trailing whitespace is trimmed for
//     unquoted values, preserved for quoted.
//   - The `export ` prefix is stripped only when followed by whitespace and
//     a valid identifier. `exportFOO=bar` is a bare assignment.
//   - YAML-style `KEY: value` and backtick-quoted values are dialect
//     extensions; reject them with [WithStrictDotenv] or accept them with
//     the relevant opt-in.
//
// Live reload:
//
//   - Editor "atomic save" patterns (write-temp-then-rename) break naive
//     fsnotify watches because the new inode is a different file. The
//     env/dotenv/watcher sub-package watches the parent directory and
//     filters by filename — the only correct strategy.
//   - The reload pipeline parses + validates into a fresh candidate before
//     swapping; failures retain the previous value and emit an error
//     event. The struct returned by [Live.Get] is therefore always a
//     successfully-validated snapshot.
//   - Treat the struct returned by [Live.Get] as IMMUTABLE. The whole point
//     of the [sync/atomic.Pointer] swap is that readers can rely on a
//     stable view; mutating it through the returned pointer is a race.
//   - Fields tagged `immutable` reject reload candidates that would change
//     them. Initial load is exempt; subsequent reloads compare against the
//     last successful snapshot.
//
// Concurrency:
//
//   - [Loader] is safe for concurrent use after construction.
//   - [Loader.Watch] mutates v in place from the worker goroutine — readers
//     must NOT touch v concurrently. For lock-free reads use [NewLive]
//     instead.
//   - [Source] implementations are required to make Lookup safe for
//     concurrent use; built-in sources do.
//
// Test isolation:
//
//   - Global [os.Environ] is shared state; tests that set it are NOT
//     parallel-safe even via [testing.T.SetEnv]. For parallelizable tests,
//     prefer [Map] over [OSEnv] as the loader's source.
//
// Encoding (reverse direction):
//
//   - [Encode] is a tooling feature, not a primary API. Secret-tagged fields
//     redact to "***" by default; [WithEncodeIncludeSecrets] opts in.
//     Encode + Load is byte-stable for canonical inputs but is not a
//     round-trip for `.env` FILES (comments and original quoting are lost).
//     For lossless `.env` editing, use dotenv.Parse → [(*File).Set] →
//     [(*File).Marshal].
//
// Architecture:
//
//   - The root package has zero third-party runtime dependencies. `.env`
//     parsing lives in env/dotenv (no third-party deps either); fsnotify-
//     backed watching lives in env/dotenv/watcher. Consumers who only need
//     OSEnv + Map pay no transitive cost. Verified by `go list -deps`.
package env
