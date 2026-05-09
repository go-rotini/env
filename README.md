# go-rotini/env

A Go environment-variable loader that treats env vars the way `encoding/json` treats JSON: a struct gets populated via tags, with first-class support for live reload, multiple sources, and `.env` files.

This package is used as the default environment-variable support package for [rotini](https://github.com/go-rotini/rotini).

## Features

- One-line happy path: `env.Load(&cfg)` is enough for most programs
- `encoding/json`-shaped API: single comma-separated `env:"NAME,opt1,opt2"` tag, generic `LoadTo[T]` convenience, `Encoder`-style `Loader` for advanced cases
- Aggregated multi-error reporting with source-and-line attribution for `.env` parse failures
- Principled `Source` interface: `OSEnv()`, `Map`, `Multi(...)` in the root, `.env` parsing in `env/dotenv`, fsnotify-backed watching in `env/dotenv/watcher`
- Atomic, lock-free reads on reload via `Live[T]` and `sync/atomic.Pointer`
- Per-field `immutable` enforcement: reload candidates that change a marked field are rejected
- Secret-redacting output (`String()`, `Describe`, `PrintUsage`, `Markdown`, `Encode`) plus a `Secret[T]` wrapper integrating with `slog.LogValuer`
- Variable expansion: `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:?error}` (opt-in)
- Built-in support for `time.Duration`, `time.Time`, `*time.Location`, `url.URL`, `net.IP`, `*net.IPNet`, `regexp.Regexp`, `[]byte` (raw, base64, hex), plus `encoding.TextUnmarshaler` and `encoding.BinaryUnmarshaler` interop
- Mutator chain for value rewriting (resolving `secret://` indirections, fetching from a vault) and observation (logging, metrics)
- `Validate() error` lifecycle hook for cross-field rules
- Auto-generated help text (`PrintUsage`) and Markdown documentation (`Markdown`) from a tagged struct
- Lossless round-trip for `.env` editing through the `dotenv.File` AST
- Reverse direction: `Encode` / `EncodeFile` write a struct out as `KEY=VALUE` lines suitable for child-process env or `.env.dist` files
- DoS guards on the `.env` parser: max file size, max line length, expansion depth, cycle detection
- Zero non-stdlib dependencies in the root; fsnotify isolated to `env/dotenv/watcher`

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

The `env/dotenv` sub-package parses `.env` files into an `env.Source`:

```go
import (
	"github.com/go-rotini/env"
	"github.com/go-rotini/env/dotenv"
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
Pair it with the fsnotify-backed watcher for automatic reload on file edits:

```go
import (
	"github.com/go-rotini/env"
	"github.com/go-rotini/env/dotenv/watcher"
)

watch, err := watcher.New(".env")
if err != nil { log.Fatal(err) }
defer watch.Close()

live, err := env.NewLive[Config](env.WithSource(env.OSEnv(), watch))
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

The watcher debounces fsnotify burst events (default 75ms) and handles the
"editor atomic save" pattern (write-temp-then-rename) by watching the parent
directory and filtering by filename.

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

For lossless `.env` editing (preserving comments and original quoting), use
`dotenv.Parse` → `(*File).Set` → `(*File).Marshal` instead — `Encode` is a
struct-to-text path and does not preserve formatting.

## Multiple sources and precedence

Source chains are explicit and ordered — the first source to return ok=true
wins. The cleanest mental model is "process env wins over `.env` file unless
told otherwise" — list `OSEnv()` first:

```go
loader := env.New(env.WithSource(
	env.OSEnv(),                                    // wins by default
	mustDotenv(".env.local"),                       // dev overrides
	mustDotenv(".env"),                             // baseline
))
```

`WithSourceAppend` and `WithSourcePrepend` adjust an existing chain without
replacing it.

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

On reload, the validator runs against the new candidate; failure retains the
previous value.

## Documentation

Full API reference is available on [pkg.go.dev](https://pkg.go.dev/github.com/go-rotini/env).

The package's design rationale and edge-case catalogue live in
[`.docs/ENV_PACKAGE_REQUIREMENTS.md`](.docs/ENV_PACKAGE_REQUIREMENTS.md) — read
§12 ("Edge Cases, Traps & Implementation Notes") before integrating in
production.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to contribute to this project.

## Code of Conduct

This project follows a code of conduct to ensure a welcoming community. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Security

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
