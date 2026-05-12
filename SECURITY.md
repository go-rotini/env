# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it responsibly by emailing **matthewcgetz@gmail.com**. Do not open a public issue.

You should receive a response within 72 hours. If accepted, a fix will be developed privately and released as a patch version.

## Resource Limits

This package defaults to safe behavior to mitigate denial-of-service and accidental misuse:

- **Variable-expansion cycles** in `expand`-tagged values are detected and rejected with `ErrCycle` rather than overflowing the stack.
- **`fromFile` reads** read the named file's contents into the field. Only fields you explicitly tag `fromFile` are eligible; keep the path under your control (don't derive it from untrusted input, e.g. pointed at `/dev/zero`).
- **No subshell execution.** The package never invokes `$(command)` or any shell substitution form.
- **No `os.Setenv` from inside the library** (with the single exception of the `unset` tag option, which is opt-in per field). All "writes" operate on in-memory `Source` chains or `*atomic.Pointer[T]` snapshots so the package never collides with libc `setenv`/`getenv` thread-safety on cgo paths.
- **`.env` parsing** is not part of this package — it lives in [go-rotini/dotenv](https://github.com/go-rotini/dotenv), which has its own DoS guards (max file size, max line length, expansion depth, cycle detection). See that package's `SECURITY.md`.

## Secret Handling

Fields tagged `secret` are redacted in:

- `Describe` / `PrintUsage` / `Markdown` output
- Error messages (replaced with `<redacted: N bytes>`)
- `Encode` output (replaced with `***` unless `WithEncodeIncludeSecrets(true)` is explicitly set)

The `Secret[T]` wrapper threads redaction through `fmt.Stringer`, `fmt.GoStringer`, and `slog.LogValuer` so secret values do not leak through user-side logging.
