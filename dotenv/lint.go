package dotenv

import (
	"errors"
	"strings"

	"github.com/go-rotini/env"
)

// Severity classifies a [LintIssue] as either an error (genuine syntax
// violation) or a warning (portable-subset deviation).
type Severity int

const (
	// SeverityError marks a [LintIssue] that prevented parsing.
	SeverityError Severity = iota

	// SeverityWarning marks a [LintIssue] that parsed successfully but
	// uses a non-portable construct (e.g., YAML-style `:` separator,
	// `export` prefix, backtick quoting, hyphenated identifier names).
	SeverityWarning
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	}
	return "unknown"
}

// LintIssue describes a single deviation from the portable `.env` subset.
// Constructs flagged include the `export ` prefix, YAML-style `KEY: value`
// separator, identifiers outside the POSIX [A-Za-z_][A-Za-z0-9_]* class,
// and backtick-quoted values.
type LintIssue struct {
	Pos      env.Position
	Rule     string
	Message  string
	Severity Severity
}

// Lint reports every construct in data that falls outside the portable
// `.env` subset. Severity-Warning issues parsed successfully but use
// dialect-specific features; Severity-Error issues prevented parsing.
//
// The returned slice may contain warnings even when err is non-nil —
// the linter still flags non-portable constructs encountered before
// the parse failure.
func Lint(data []byte, opts ...ParseOption) ([]LintIssue, error) {
	o := defaultParseOptions()
	for _, opt := range opts {
		opt(&o)
	}

	var issues []LintIssue
	collect := func(rule, msg string, pos env.Position, sev Severity) {
		issues = append(issues, LintIssue{
			Pos:      pos,
			Rule:     rule,
			Message:  msg,
			Severity: sev,
		})
	}

	// First pass: scan for non-portable constructs by walking the source
	// directly. We do this BEFORE parsing so even files that fail to
	// parse can produce useful warnings up to the point of failure.
	scanForLintWarnings(data, o, collect)

	// Second pass: try to parse. A parse error is itself a lint error.
	if _, err := parse(data, o); err != nil {
		var pe *ParseError
		if asParseError(err, &pe) {
			collect("syntax", pe.Message, pe.Pos, SeverityError)
		} else {
			collect("syntax", err.Error(), env.Position{}, SeverityError)
		}
		return issues, err
	}
	return issues, nil
}

// asParseError extracts a *ParseError from err. Uses [errors.As] so
// arbitrarily-wrapped errors still resolve.
func asParseError(err error, dst **ParseError) bool {
	return errors.As(err, dst)
}

// scanForLintWarnings walks src looking for constructs outside the
// portable subset and reports each via collect.
func scanForLintWarnings(src []byte, opts parseOptions, collect func(rule, msg string, pos env.Position, sev Severity)) {
	src = stripUTF8BOM(src)
	line := 1
	col := 1
	i := 0
	for i < len(src) {
		// Find the next physical line.
		lineStart := i
		for i < len(src) && src[i] != '\n' && src[i] != '\r' {
			i++
		}
		raw := string(src[lineStart:i])
		// Consume the line ending.
		if i < len(src) {
			if src[i] == '\r' {
				i++
				if i < len(src) && src[i] == '\n' {
					i++
				}
			} else {
				i++
			}
		}
		_ = col

		// Lint this physical line.
		lintLine(raw, line, opts, collect)
		line++
	}
}

// lintLine inspects one physical line. Multi-line double-quoted values
// will produce noisy warnings on their continuation lines; that's
// acceptable for a static check (multi-line values aren't in the
// portable subset either).
func lintLine(raw string, lineNo int, opts parseOptions, collect func(rule, msg string, pos env.Position, sev Severity)) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return
	}
	pos := env.Position{File: opts.filePath, Line: lineNo, Column: 1}

	// `export ` prefix — rejected in strict mode; non-portable in motdotla.
	if strings.HasPrefix(trimmed, "export ") {
		collect("export-prefix", "`export ` prefix is not portable to motdotla/dotenv",
			pos, SeverityWarning)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}

	// Find separator.
	eqIdx := strings.IndexByte(trimmed, '=')
	colonIdx := strings.IndexByte(trimmed, ':')
	if colonIdx >= 0 && (eqIdx < 0 || colonIdx < eqIdx) {
		collect("yaml-separator",
			"`KEY: VALUE` form is not portable; use `KEY=VALUE`",
			pos, SeverityWarning)
	}

	// Hyphen / dot in identifier — non-portable.
	keyEnd := eqIdx
	if colonIdx >= 0 && (keyEnd < 0 || colonIdx < keyEnd) {
		keyEnd = colonIdx
	}
	if keyEnd > 0 {
		key := trimmed[:keyEnd]
		key = strings.TrimRight(key, " \t")
		for _, r := range key {
			if r == '-' || r == '.' {
				collect("non-posix-identifier",
					"identifier "+strconvQuote(key)+" contains non-POSIX character",
					pos, SeverityWarning)
				break
			}
		}
	}

	// Backtick value — non-portable.
	if strings.Contains(trimmed, "`") {
		collect("backtick-value",
			"backtick-quoted value is not portable; use double quotes",
			pos, SeverityWarning)
	}
}

// strconvQuote returns s wrapped in double quotes. A tiny helper so the
// import list stays minimal — strconv would do the job too.
func strconvQuote(s string) string {
	return "\"" + s + "\""
}
