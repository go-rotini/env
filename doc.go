// Package env loads environment variables into Go structs, with
// first-class support for live reload.
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
// one source; `.env` files (via the separate github.com/go-rotini/dotenv
// package), in-memory maps ([Map]), source chains ([Multi]), and any
// custom [Source] are equal citizens.
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
// # Related packages
//
// This package has no third-party runtime dependencies and no sub-packages.
// Adjacent functionality lives in its own module so consumers pay only for
// what they import:
//
//   - github.com/go-rotini/dotenv — `.env` file parser, AST, lossless
//     round-trip, Lint. Its NewSource returns a value whose Lookup method
//     is structurally compatible with [Source], so a parsed `.env` file
//     plugs into a loader's source chain — dotenv does not import this
//     package (or anything else; it has no dependencies).
//   - github.com/go-rotini/fs — filesystem helpers including a
//     content-agnostic file watcher (atomic-rename aware, debounced).
//     Compose it with dotenv (or a yaml/toml/jsonc parser) into a [Source]
//     that also implements [Watcher] to drive live reload of config files
//     through [NewLive].
//
// # Tag system
//
// A single comma-separated tag, mirroring [encoding/json]:
//
//	`env:"NAME,opt1,opt2"`
//
// Supported options: required, notEmpty, default=value, separator=char,
// kvSeparator=char, expand, fromFile, secret, immutable, layout=format,
// base64, hex, unset. (`expand` performs `$VAR` / `${VAR}` / `${VAR-default}`
// / `${VAR:-default}` / `${VAR:?message}` substitution on the resolved value,
// resolving names verbatim against the source chain; unset references expand
// to "". `${VAR:?msg}` for an unset VAR fails with [ErrExpansionAssertion];
// the error includes the expanded message, except on secret fields.
// As with [os.Expand]/POSIX shells, inside `${...}` a `-` (or `:-`, `:?`)
// starts the operator section, so a variable whose name contains `-` cannot
// be referenced with the `${...}` form — use a name without `-`, or `$NAME`
// only if no `-` follows. `WithPrefix` is NOT applied to expansion
// references.) Companion tags: envDesc:"..." for free-text help and
// envPrefix:"PFX_" on nested struct fields. An embedded struct's fields
// are promoted without a prefix; an env tag on the embed is ignored
// (except "-"), so use envPrefix to namespace them. See the package README
// for the full table.
//
// # Pitfalls
//
// Environment variables come with platform-specific quirks. The list below
// covers the issues most likely to bite a new user.
//
// OS-level:
//
//   - Windows matches env-var names case-insensitively; POSIX is
//     case-sensitive. The [OSEnv] source matches the platform — for
//     cross-platform tests, use [Map] with [WithCaseInsensitive].
//   - [os.Getenv] collapses unset and empty into "". The [Source] interface
//     preserves the distinction via the second bool return; [OSEnv] uses
//     [os.LookupEnv] to honor it. The `required` tag option is satisfied by
//     a set-but-empty value; `notEmpty` requires both set and non-empty.
//   - [os.Setenv] / [os.Unsetenv] are not thread-safe under cgo (golang/go
//     #63567). This package never calls them from library code; the `unset`
//     tag option is the single opt-in exception, serialized within a Load
//     call. It removes the variable as soon as it is read (even if the
//     value or another field then fails, so a retried Load, Watch or
//     NewLive no longer finds it), and only when the process environment
//     holds the loaded value. Disable globally via [WithoutUnset] in test
//     environments.
//   - [os.Environ] returns degenerate entries on Windows (empty keys,
//     leading-`=` per-drive variables). [OSEnv] filters them; you don't see
//     them via [Source.Lookup].
//
// Live reload:
//
//   - Reload is driven by any [Source] that also implements [Watcher].
//     Sources that don't are polled at the interval set by
//     [WithPollInterval], or read once and never re-read if that option is
//     absent. For file-backed config (`.env` / yaml / toml / jsonc), wrap the
//     parser in a [Source] + [Watcher] built on github.com/go-rotini/fs's file
//     watcher, which handles the editor "atomic save" pattern
//     (write-temp-then-rename) by watching the parent directory and filtering
//     by filename — the only correct strategy, since the rewritten file is a
//     new inode.
//   - The reload pipeline parses + validates into a fresh candidate before
//     swapping; failures retain the previous value and emit an error
//     event. The struct returned by [Live.Get] is therefore always a
//     successfully-validated snapshot.
//   - Treat the struct returned by [Live.Get] as IMMUTABLE. The whole point
//     of the [sync/atomic.Pointer] swap is that readers can rely on a
//     stable view; mutating it through the returned pointer is a race.
//   - Fields tagged `immutable` reject reload candidates that would change
//     them. Initial load is exempt; subsequent reloads compare against the
//     last successful load. The comparison (and [Event.Changed]) uses the
//     string each field was decoded from, after defaults, mutators,
//     expansion and fromFile, so a change in a referenced variable, a
//     mutator's result or a file's contents counts.
//   - The `unset` tag option removes a variable from the process
//     environment during the first load. Reloads reuse the removed value
//     rather than seeing the variable as missing.
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
//     parallel-safe even via [testing.T.Setenv]. For parallelizable tests,
//     prefer [Map] over [OSEnv] as the loader's source.
//
// Encoding (reverse direction):
//
//   - [Encode] is a tooling feature, not a primary API. Secret-tagged and
//     [Secret] fields redact to "***" by default; [WithEncodeIncludeSecrets] opts in.
//     A slice or map element that contains its separator cannot be
//     written so that it loads back, so Encode returns
//     [ErrSeparatorInValue] for it.
//     Encode + Load is byte-stable for canonical inputs but is not a
//     round-trip for `.env` FILES (comments and original quoting are lost).
//     For lossless `.env` editing, use github.com/go-rotini/dotenv's Parse →
//     (*File).Set → (*File).Marshal.
//
// Architecture:
//
//   - This package has zero third-party runtime dependencies and no
//     sub-packages. `.env` parsing is in github.com/go-rotini/dotenv (also
//     dependency-free); file watching is in github.com/go-rotini/fs.
//     Consumers who only need OSEnv + Map pay no transitive cost.
package env
