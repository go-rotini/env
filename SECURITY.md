# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it responsibly by emailing **matthewcgetz@gmail.com**. Do not open a public issue.

You should receive a response within 72 hours. If accepted, a fix will be developed privately and released as a patch version.

## Resource Limits

This package defaults to safe behavior to mitigate denial-of-service and accidental misuse:

- **`.env` parser DoS guards**: configurable via `WithMaxFileSize`, `WithMaxLineLength`, and `WithMaxExpansionDepth`. Defaults are conservative (10 MiB file size, 1 MiB line length, expansion depth 16).
- **Variable-expansion cycles** are detected and rejected with `ErrCycle` rather than overflowing the stack.
- **`fromFile` reads** are bounded by `WithFromFileMaxSize` (default 1 MiB) so a path pointed at `/dev/zero` cannot exhaust memory.
- **No subshell execution.** The package never invokes `$(command)` or any shell substitution form, regardless of dialect.
- **No `os.Setenv` from inside the library** (with the single exception of the `unset` tag option, which is opt-in per field). All "writes" operate on in-memory `Source` chains or `*atomic.Pointer[T]` snapshots so the package never collides with libc `setenv`/`getenv` thread-safety on cgo paths.

## Secret Handling

Fields tagged `secret` are redacted in:

- `Describe` / `PrintUsage` / `Markdown` output
- Error messages (replaced with `<redacted: N bytes>`)
- `Encode` output (replaced with `***` unless `WithEncodeIncludeSecrets(true)` is explicitly set)

The `Secret[T]` wrapper threads redaction through `fmt.Stringer`, `fmt.GoStringer`, and `slog.LogValuer` so secret values do not leak through user-side logging.
