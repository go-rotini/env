# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Phase 1: project scaffolding — config files, GitHub workflows, Makefile, package documentation skeleton.
- Phase 2: `Source` interface, `Watcher` sub-interface, `Change` struct, plus root-package built-ins `OSEnv()`, `Map(m, opts...)` (with `WithCaseInsensitive` option), and `Multi(srcs...)`. `Source` is the bedrock contract through which the loader resolves env-var values; `OSEnv` reads from `os.LookupEnv` and rejects degenerate keys (empty / leading `=` / containing `=` or NUL); `Map` copies its input at construction; `Multi` delivers first-match-wins precedence. 100% statement coverage; race-clean.
