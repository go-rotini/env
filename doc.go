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
// Environment variables come with platform-specific quirks documented at
// length in the requirements document and reproduced here in summary:
//
//   - Windows env-var names are case-insensitive; POSIX is case-sensitive.
//     The OSEnv source matches the platform.
//   - os.Getenv collapses unset and empty; the Source interface preserves the
//     distinction via the second bool return.
//   - os.Setenv is not thread-safe under cgo. The package never calls
//     os.Setenv from inside library code (the unset tag option is the single
//     opt-in exception).
//   - On macOS, fsnotify-backed watching of a single file dies after an
//     atomic-rename save (Vim, Helix, many IDEs). The env/dotenv/watcher
//     sub-package watches the parent directory and filters by filename to
//     handle this.
//   - Secret-tagged fields are redacted in error messages, Describe output,
//     and Encode output by default. Round-trips through Encode + Load are
//     therefore lossy for secrets unless WithEncodeIncludeSecrets is set.
package env
