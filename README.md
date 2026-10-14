# go-rotini/env

A Go environment-variable loader that treats env vars the way `encoding/json` treats JSON: a struct gets populated via tags, with first-class support for live reload and pluggable sources — OS environment, in-memory maps, ordered chains, and `.env` files (via the separate [go-rotini/dotenv](https://github.com/go-rotini/dotenv) package).

This package is used as the default environment-variable support package for [rotini](https://github.com/go-rotini/rotini).

## Features

- One-line happy path: `env.Load(&cfg)` is enough for most programs
- `encoding/json`-shaped API: single comma-separated `env:"NAME,opt1,opt2"` tag, generic `LoadTo[T]` convenience, a configurable `Loader` (the `Decoder`-style entry point) for advanced cases
- Aggregated multi-error reporting with per-field attribution (`*MultiError`, `FormatError`)
- Principled `Source` interface: `OSEnv()`, `Map`, `Multi(...)` built in; `.env` parsing via [go-rotini/dotenv](https://github.com/go-rotini/dotenv); any other store (such as a secret manager) plugs in by implementing `Source`
- `context.Context` support: `LoadContext` threads the context to mutators and to `UnmarshalerContext` / `ValidatorContext` hooks
- Atomic, lock-free reads on reload via `Live[T]` and `sync/atomic.Pointer`
- Per-field `immutable` enforcement: reload candidates that change a marked field are rejected (old/new values redacted for secret, `fromFile` and `expand` fields, and for every field when a mutator is set)
- Secret-redacting output (`String()`, `Describe`, `PrintUsage`, `Markdown`, `Encode`) plus a `Secret[T]` wrapper integrating with `slog.LogValuer`
- Variable expansion on tagged values (`expand` tag): `$VAR`, `${VAR}`, `${VAR-default}`, `${VAR:-default}`, `${VAR:?error}` — references resolve against the source chain, used verbatim (no `WithPrefix` applied)
- Built-in support for `time.Duration`, `time.Time`, `*time.Location`, `url.URL`, `net.IP`, `*net.IPNet`, `regexp.Regexp`, `[]byte` (raw, base64, hex), plus `encoding.TextUnmarshaler` and `encoding.BinaryUnmarshaler` interop
- Mutator chain for value rewriting (resolving `secret://` indirections, fetching from a vault) and observation (logging, metrics)
- `Validate() error` lifecycle hook for cross-field rules
- Auto-generated help text (`PrintUsage`) and Markdown documentation (`Markdown`) from a tagged struct
- Reverse direction: `Encode` / `EncodeFile` write a struct out as `KEY=VALUE` lines suitable for child-process env or `.env.dist` files
- Zero non-stdlib runtime dependencies; no sub-packages

## Installation

```bash
go get github.com/go-rotini/env
```

Requires Go 1.26 or later.

## Quick Start

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/go-rotini/env"
)

type Config struct {
	Port    int           `env:"PORT,default=8080"        envDesc:"HTTP listen port"`
	DBURL   string        `env:"DATABASE_URL,required"    envDesc:"Primary database DSN"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"      envDesc:"Request timeout"`
	Hosts   []string      `env:"HOSTS,separator=;"        envDesc:"Allowed host list"`
	APIKey  string        `env:"API_KEY,required,secret"  envDesc:"Upstream API credential"`
}

func main() {
	// One-shot load (default Loader, OSEnv source, no prefix).
	var cfg Config
	if err := env.Load(&cfg); err != nil {
		log.Fatal(env.FormatError(err))
	}
	fmt.Printf("%+v\n", cfg)

	// Generic convenience.
	cfg2, err := env.LoadTo[Config]()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%+v\n", cfg2)
}
```

## Loading from a `.env` file

`.env` parsing lives in the separate [`github.com/go-rotini/dotenv`](https://github.com/go-rotini/dotenv)
package (which has no dependencies). Its `NewSource` returns a value usable directly
as a `Source` — dotenv doesn't import this package; the method sets just line up:

```go
import (
	"github.com/go-rotini/env"
	"github.com/go-rotini/dotenv"
)

src, err := dotenv.NewSource(".env", dotenv.WithExpand())
if err != nil { log.Fatal(err) }

// OSEnv() listed first wins over the .env file (the standard Compose / Heroku
// precedence model). Reverse the order to make the file authoritative.
loader := env.New(env.WithSource(env.OSEnv(), src))

var cfg Config
if err := loader.Load(&cfg); err != nil {
	log.Fatal(env.FormatError(err))
}
```

## Live reload

`Live[T]` wraps the loader in an `atomic.Pointer[T]`-backed handle. Reads are
O(1) and lock-free; readers always observe a complete, validated snapshot.
Reload is driven by any `Source` that also implements `env.Watcher`; sources
that don't are polled at the interval set by `WithPollInterval`, or read once
and never re-read if that option is absent.

```go
live, err := env.NewLive[Config](env.WithSource(env.OSEnv(), changingSrc))
if err != nil { log.Fatal(err) }
defer live.Close()

// Hot-path read.
go func() {
	for {
		cfg := live.Get() // *Config — never nil after NewLive succeeds
		serve(cfg)
	}
}()

// Optional event observation.
go func() {
	for ev := range live.Events() {
		if ev.Err != nil {
			log.Printf("reload failed: %v", ev.Err)
			continue
		}
		log.Printf("reload: changed=%v", ev.Changed)
	}
}()
```

For automatic reload on `.env` (or yaml/toml/jsonc) file edits, use a live
source built on [`github.com/go-rotini/fs`](https://github.com/go-rotini/fs)'s
file watcher — it debounces editor save bursts and handles the "atomic save"
pattern (write-temp-then-rename) by watching the parent directory and filtering
by filename. The reload pipeline parses + validates a fresh candidate before
swapping; a failed candidate retains the previous value and emits an error
event. A poll that finds nothing changed emits no event, unless the previous
reload failed.

## Auto-generated documentation

`Describe` / `PrintUsage` / `Markdown` walk the same field pipeline as `Load`
but emit documentation instead of populating a struct. Wire them into a
`--env-help` flag or a docs-generation step:

```go
// Print a column-aligned help table to stderr.
_ = env.PrintUsage(os.Stderr, &Config{})

// Generate a GitHub-Flavored Markdown reference.
_ = env.Markdown(os.Stdout, &Config{})
```

## Reverse direction (Encode)

`Encode` writes a struct out as `KEY=VALUE` lines — useful for child-process
env, audit snapshots, or generating an example `.env.dist` file:

```go
// Default redacts secrets as ***.
_ = env.Encode(os.Stdout, &cfg)

// For child-process exec.
buf := &bytes.Buffer{}
_ = env.Encode(buf, &cfg, env.WithEncodeIncludeSecrets(true))
cmd.Env = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
```

The package-level `Encode` / `EncodeFile` use the defaults (`env` tag,
SCREAMING_SNAKE_CASE, no prefix). If you built the loader with `WithTagName`,
`WithFieldNameTransform`, or `WithPrefix`, use the loader methods
`loader.Encode` / `loader.EncodeFile` so the output round-trips through
`loader.Load`.

For lossless `.env` editing (preserving comments and original quoting), use
[`github.com/go-rotini/dotenv`](https://github.com/go-rotini/dotenv)'s `Parse` →
`(*File).Set` → `(*File).Marshal` instead — `Encode` is a struct-to-text path
and does not preserve formatting.

## Multiple sources and precedence

Source chains are explicit and ordered — the first source to return ok=true
wins. The cleanest mental model is "process env wins over `.env` file unless
told otherwise" — list `OSEnv()` first:

```go
loader := env.New(env.WithSource(
	env.OSEnv(),                                    // wins by default
	mustDotenv(".env.local"),                       // dev overrides   (dotenv.NewSource)
	mustDotenv(".env"),                             // baseline        (dotenv.NewSource)
))
```

(`mustDotenv` here is a thin wrapper around
[`github.com/go-rotini/dotenv`](https://github.com/go-rotini/dotenv)'s
`NewSource` that panics on error.) `WithSourceAppend` and `WithSourcePrepend`
adjust an existing chain without replacing it.

## Validation

Implement `Validate() error` on the config type for cross-field rules. The
loader invokes it after every field has populated and reports its return value
through the standard `MultiError` aggregation:

```go
func (c *Config) Validate() error {
	if c.Port < 1024 && os.Geteuid() != 0 {
		return fmt.Errorf("PORT %d requires root", c.Port)
	}
	return nil
}
```

For validation that needs the request context, implement `Validate(ctx context.Context) error`
instead (the `ValidatorContext` interface) — the loader passes the context given to
`LoadContext` (or `context.Background()` for plain `Load`). A type implements one or the other,
not both. On reload, the validator runs against the new candidate; failure retains the previous
value.

Likewise, a custom field type can implement `UnmarshalEnv(ctx context.Context, s string) error`
(`UnmarshalerContext`) to receive the context during decoding, instead of the plain
`UnmarshalEnv(s string) error` (`Unmarshaler`).

## Documentation

Full API reference is available on [pkg.go.dev](https://pkg.go.dev/github.com/go-rotini/env).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to contribute to this project.

## Code of Conduct

This project follows a code of conduct to ensure a welcoming community. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Security

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
