package dotenv

import "github.com/go-rotini/env"

// ParseOption configures [Parse], [Load], [NewSource],
// [NewReaderSource], and [Lint].
type ParseOption func(*parseOptions)

type parseOptions struct {
	expand            bool
	strictExpansion   bool
	maxExpansionDepth int
	expansionParent   env.Source
	backticks         bool
	strictDotenv      bool
	maxFileSize       int64
	maxLineLength     int
	relaxedNames      bool
	filePath          string
}

func defaultParseOptions() parseOptions {
	return parseOptions{
		maxExpansionDepth: 16,
		maxFileSize:       10 * 1024 * 1024, // 10 MiB
		maxLineLength:     1024 * 1024,      // 1 MiB
	}
}

// WithExpand enables Bash-style variable expansion in unquoted and
// double-quoted values: `$VAR`, `${VAR}`, `${VAR:-default}`,
// `${VAR-default}`, `${VAR:?error}`. Backslash-dollar (`\$`) suppresses
// expansion. Lookups walk the file's already-parsed entries first, then
// any parent set via [WithExpansionParent].
func WithExpand() ParseOption {
	return func(o *parseOptions) { o.expand = true }
}

// WithStrictExpansion makes a reference to an unset variable raise a
// [*ExpansionError] rather than expand to empty. Has no effect unless
// [WithExpand] is also set.
func WithStrictExpansion() ParseOption {
	return func(o *parseOptions) { o.strictExpansion = true }
}

// WithMaxExpansionDepth bounds variable-expansion recursion. Default 16.
// Cycles surface as [*ExpansionError] before the bound is exceeded; the
// bound itself only matters for genuinely deep but non-cyclic references.
func WithMaxExpansionDepth(n int) ParseOption {
	return func(o *parseOptions) {
		if n > 0 {
			o.maxExpansionDepth = n
		}
	}
}

// WithExpansionParent sets the parent [env.Source] consulted by variable
// expansion when a referenced variable is not present in the file's own
// entries. Typical use: [env.OSEnv] so the file can reference
// process-env vars.
func WithExpansionParent(parent env.Source) ParseOption {
	return func(o *parseOptions) { o.expansionParent = parent }
}

// WithBackticks accepts backtick-quoted values (motdotla/dotenv dialect).
// Treated like single-quoted: literal, no escapes, no expansion. Default
// is to reject as a syntax error.
func WithBackticks() ParseOption {
	return func(o *parseOptions) { o.backticks = true }
}

// WithStrictDotenv rejects non-portable constructs: YAML-style
// `KEY: value` separators, the `export ` prefix, identifiers outside
// the POSIX class. Useful for asserting cross-dialect portability.
func WithStrictDotenv() ParseOption {
	return func(o *parseOptions) { o.strictDotenv = true }
}

// WithMaxFileSize caps the total input size. Default 10 MiB. Zero or
// negative means unlimited.
func WithMaxFileSize(n int64) ParseOption {
	return func(o *parseOptions) { o.maxFileSize = n }
}

// WithMaxLineLength caps a single physical line's length. Default 1 MiB.
// Zero or negative means unlimited. Multi-line double-quoted values
// count toward the line limit per physical line, not in aggregate.
func WithMaxLineLength(n int) ParseOption {
	return func(o *parseOptions) { o.maxLineLength = n }
}

// WithRelaxedNames accepts identifiers containing characters outside the
// POSIX [A-Za-z_][A-Za-z0-9_]* class (hyphens, dots, non-ASCII).
func WithRelaxedNames() ParseOption {
	return func(o *parseOptions) { o.relaxedNames = true }
}
