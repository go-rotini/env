package dotenv

import (
	"errors"
	"strings"

	"github.com/go-rotini/env"
)

// Severity classifies a [LintIssue].
type Severity int

const (
	// SeverityError marks a [LintIssue] that prevented parsing.
	SeverityError Severity = iota

	// SeverityWarning marks a parseable but non-portable construct
	// (YAML-style `:`, `export` prefix, backtick quoting, hyphenated
	// identifiers).
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

// LintIssue describes a deviation from the portable `.env` subset:
// `export ` prefix, YAML-style `KEY: value`, non-POSIX identifiers
// (hyphens, dots), or backtick-quoted values.
type LintIssue struct {
	Pos      env.Position
	Rule     string
	Message  string
	Severity Severity
}

// Lint reports constructs in data that fall outside the portable
// `.env` subset. The returned slice may contain warnings even when
// err is non-nil — warnings encountered before a parse failure are
// preserved.
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

	// Walk the source for non-portable constructs first so that a
	// later parse failure doesn't suppress already-detected warnings.
	scanForLintWarnings(data, o, collect)

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

func asParseError(err error, dst **ParseError) bool {
	return errors.As(err, dst)
}

func scanForLintWarnings(src []byte, opts parseOptions, collect func(rule, msg string, pos env.Position, sev Severity)) {
	src = stripUTF8BOM(src)
	line := 1
	i := 0
	for i < len(src) {
		lineStart := i
		for i < len(src) && src[i] != '\n' && src[i] != '\r' {
			i++
		}
		raw := string(src[lineStart:i])
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
		lintLine(raw, line, opts, collect)
		line++
	}
}

// lintLine inspects one physical line. Continuation lines of multi-
// line double-quoted values produce noisy warnings; that's acceptable
// since multi-line values aren't in the portable subset either.
func lintLine(raw string, lineNo int, opts parseOptions, collect func(rule, msg string, pos env.Position, sev Severity)) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return
	}
	pos := env.Position{File: opts.filePath, Line: lineNo, Column: 1}

	if strings.HasPrefix(trimmed, "export ") {
		collect("export-prefix", "`export ` prefix is not portable to motdotla/dotenv",
			pos, SeverityWarning)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}

	eqIdx := strings.IndexByte(trimmed, '=')
	colonIdx := strings.IndexByte(trimmed, ':')
	if colonIdx >= 0 && (eqIdx < 0 || colonIdx < eqIdx) {
		collect("yaml-separator",
			"`KEY: VALUE` form is not portable; use `KEY=VALUE`",
			pos, SeverityWarning)
	}

	keyEnd := eqIdx
	if colonIdx >= 0 && (keyEnd < 0 || colonIdx < keyEnd) {
		keyEnd = colonIdx
	}
	if keyEnd > 0 {
		key := strings.TrimRight(trimmed[:keyEnd], " \t")
		for _, r := range key {
			if r == '-' || r == '.' {
				collect("non-posix-identifier",
					"identifier \""+key+"\" contains non-POSIX character",
					pos, SeverityWarning)
				break
			}
		}
	}

	if strings.Contains(trimmed, "`") {
		collect("backtick-value",
			"backtick-quoted value is not portable; use double quotes",
			pos, SeverityWarning)
	}
}
