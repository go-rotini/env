# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Phase 1: project scaffolding — config files, GitHub workflows, Makefile, package documentation skeleton.
- Phase 2: `Source` interface, `Watcher` sub-interface, `Change` struct, plus root-package built-ins `OSEnv()`, `Map(m, opts...)` (with `WithCaseInsensitive` option), and `Multi(srcs...)`. `Source` is the bedrock contract through which the loader resolves env-var values; `OSEnv` reads from `os.LookupEnv` and rejects degenerate keys (empty / leading `=` / containing `=` or NUL); `Map` copies its input at construction; `Multi` delivers first-match-wins precedence. 100% statement coverage; race-clean.
- Phase 3: tag parsing, field collection, and SCREAMING_SNAKE_CASE name transform with acronym detection. `parseFieldTag` handles every option from §6.2 (`required`, `notEmpty`, `default=`, `separator=`, `kvSeparator=`, `expand`, `fromFile`, `unset`, `secret`, `immutable`, `layout=`, `base64`, `hex`); `envDesc` and `envPrefix` companion tags are recognized separately. The recursive walker handles nested structs, prefix accumulation, anonymous embeds (with optional prefix), pointer-to-struct, and built-in leaf detection (`time.Time`, `url.URL`, `net.IPNet`, `regexp.Regexp`, `big.Int/Float/Rat`, plus any type whose `*T` implements `encoding.TextUnmarshaler`/`BinaryUnmarshaler`). Per-type results are cached in a `sync.Map` keyed by `reflect.Type`. Conflict detection runs at field-collection time; tag-parse and conflict errors aggregate via `errors.Join`. 100% statement coverage; race-clean.
