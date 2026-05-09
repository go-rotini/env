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

	// Live reload — readers see atomic snapshots.
	live, err := env.NewLive[Config](env.WithSource(env.OSEnv()))
	if err != nil {
		log.Fatal(err)
	}
	defer live.Close()
	current := live.Get() // *Config — never nil after NewLive succeeds
	fmt.Printf("%+v\n", current)
}
```

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
