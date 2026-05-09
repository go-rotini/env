// Package dotenv parses `.env` files and exposes them as
// [github.com/go-rotini/env.Source] values.
//
// The package targets a conservative portable subset of the `.env`
// dialects in the wild. Every dialect-specific extension (the
// `export ` prefix, YAML-style `KEY: value`, backtick quoting, variable
// expansion, multi-line double-quoted strings) is supported but gated
// behind explicit ParseOptions or, for non-portable constructs that
// nonetheless parse, surfaced as [LintIssue] SeverityWarning entries
// from [Lint].
//
// # API summary
//
//   - [Parse] / [Load]                — bytes / file → key/value map or AST.
//   - [NewSource] / [NewReaderSource] — file / reader → [env.Source].
//   - [Lint]                          — flag non-portable constructs.
//   - [File].Get/Set/Delete/Keys/ToMap — AST navigation and mutation.
//   - [File].Marshal                  — byte-exact round-trip emission.
//
// # Dependency direction
//
// `env/dotenv` imports `github.com/go-rotini/env` for the [env.Source]
// type. The reverse import would create a cycle and is not permitted.
// This split keeps the root package free of `.env` parsing code so
// consumers who only need the OS-env source pay no transitive cost.
//
// # File watching
//
// Live reload of a `.env` file lives in the [env/dotenv/watcher]
// sub-package, which adds an fsnotify dependency. dotenv itself reads
// the file once at construction; subsequent on-disk changes do not
// propagate.
package dotenv
