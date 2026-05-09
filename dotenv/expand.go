package dotenv

import (
	"fmt"
	"strings"

	"github.com/go-rotini/env"
)

// dollarSuppressSentinel marks a literal `$` produced by a `\$`
// escape in a double-quoted value. The expander emits a literal `$`
// for sentinel-marked positions and never recurses into them.
//
// U+0007 (BELL) is used because env values practically never contain
// it; user-supplied BELL bytes pass through unaffected (they don't
// precede a `$`).
const dollarSuppressSentinel = 0x07

// expandAll runs variable expansion over every assignment entry in
// source order. Single-quoted and backtick-quoted values are skipped
// (their dialects suppress expansion). Lookups consult the file's
// already-resolved entries first, then any [WithExpansionParent].
func (f *File) expandAll(opts parseOptions) error {
	resolved := make(map[string]string)
	for i := range f.entries {
		e := &f.entries[i]
		if e.Kind != EntryAssignment {
			continue
		}
		if e.quote == '\'' || e.quote == '`' {
			continue
		}
		expanded, err := expand(e.Value, e.Pos, opts, resolved)
		if err != nil {
			return err
		}
		e.Value = expanded
		resolved[e.Key] = expanded
	}
	return nil
}

func expand(s string, pos env.Position, opts parseOptions, resolved map[string]string) (string, error) {
	return expandWithDepth(s, pos, opts, resolved, 0)
}

func expandWithDepth(s string, pos env.Position, opts parseOptions, resolved map[string]string, depth int) (string, error) {
	if depth > opts.maxExpansionDepth {
		return "", &ExpansionError{
			Pos:    pos,
			Reason: fmt.Sprintf("max expansion depth %d exceeded", opts.maxExpansionDepth),
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case dollarSuppressSentinel:
			if i+1 < len(s) && s[i+1] == '$' {
				b.WriteByte('$')
				i += 2
				continue
			}
			// Defensive: a stray sentinel shouldn't occur in practice.
			i++
		case '\\':
			// `\$` in unquoted values is accepted as a courtesy: when
			// expansion is on, treat it as suppressing the following
			// reference.
			if i+1 < len(s) && s[i+1] == '$' {
				b.WriteByte('$')
				i += 2
				continue
			}
			b.WriteByte(c)
			i++
		case '$':
			ref, ok := parseVarRef(s[i:])
			if !ok {
				b.WriteByte('$')
				i++
				continue
			}
			name := ref.name
			defaultVal := ref.defaultVal
			assertVal := ref.assertVal
			defaultIfEmpty := ref.defaultIfEmpty
			hasDefault := ref.hasDefault
			hasAssert := ref.hasAssert
			consumed := ref.consumed
			value, isSet := lookupVar(name, opts, resolved)
			needsDefault := false
			needsAssert := false
			switch {
			case !isSet && hasAssert:
				needsAssert = true
			case !isSet && hasDefault:
				needsDefault = true
			case !isSet && opts.strictExpansion:
				return "", &ExpansionError{
					Pos:      pos,
					Variable: name,
					Reason:   "unset variable referenced",
					Cause:    env.ErrUnsetExpansion,
				}
			case isSet && value == "" && hasDefault && defaultIfEmpty:
				needsDefault = true
			case isSet && value == "" && hasAssert:
				needsAssert = true
			}
			switch {
			case needsAssert:
				assertExpanded, err := expandWithDepth(assertVal, pos, opts, resolved, depth+1)
				if err != nil {
					return "", err
				}
				return "", &ExpansionError{
					Pos:      pos,
					Variable: name,
					Reason:   assertExpanded,
					Cause:    env.ErrExpansionAssertion,
				}
			case needsDefault:
				expandedDefault, err := expandWithDepth(defaultVal, pos, opts, resolved, depth+1)
				if err != nil {
					return "", err
				}
				b.WriteString(expandedDefault)
			default:
				b.WriteString(value)
			}
			i += consumed
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

type varRef struct {
	name           string
	defaultVal     string
	assertVal      string
	defaultIfEmpty bool
	hasDefault     bool
	hasAssert      bool
	consumed       int
}

// parseVarRef parses one reference at the start of s. ok=false when s
// does not begin with a recognizable reference (lone `$`,
// `$<non-ident>`, unterminated `${`). Recognized shapes:
//
//	$NAME
//	${NAME}
//	${NAME:-DEFAULT}     hasDefault, defaultIfEmpty
//	${NAME-DEFAULT}      hasDefault
//	${NAME:?MSG}         hasAssert
func parseVarRef(s string) (varRef, bool) {
	if len(s) < 2 || s[0] != '$' {
		return varRef{}, false
	}
	if s[1] != '{' {
		end := 1
		for end < len(s) && isVarRefByte(s[end]) {
			end++
		}
		if end == 1 {
			return varRef{}, false
		}
		return varRef{name: s[1:end], consumed: end}, true
	}
	closeIdx := findMatchingBrace(s[2:])
	if closeIdx < 0 {
		return varRef{}, false
	}
	body := s[2 : 2+closeIdx]
	consumed := 2 + closeIdx + 1

	opIdx, opLen, op := firstTopLevelOperator(body)
	if opIdx < 0 {
		return varRef{name: body, consumed: consumed}, true
	}
	switch op {
	case ":-":
		return varRef{name: body[:opIdx], defaultVal: body[opIdx+opLen:], defaultIfEmpty: true, hasDefault: true, consumed: consumed}, true
	case ":?":
		return varRef{name: body[:opIdx], assertVal: body[opIdx+opLen:], hasAssert: true, consumed: consumed}, true
	case "-":
		return varRef{name: body[:opIdx], defaultVal: body[opIdx+opLen:], hasDefault: true, consumed: consumed}, true
	}
	return varRef{name: body, consumed: consumed}, true
}

// findMatchingBrace returns the index in s of the `}` matching the
// outer-scope `{`, balancing nested `${...}`. Returns -1 when none.
func findMatchingBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
			}
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// firstTopLevelOperator finds the first ":-", ":?", or "-" at the top
// level of body (not inside a nested `${...}`). Returns
// (-1, 0, "") when none.
func firstTopLevelOperator(body string) (int, int, string) {
	depth := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch c {
		case '\\':
			if i+1 < len(body) {
				i++
			}
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 && i+1 < len(body) {
				switch body[i+1] {
				case '-':
					return i, 2, ":-"
				case '?':
					return i, 2, ":?"
				}
			}
		case '-':
			if depth == 0 {
				return i, 1, "-"
			}
		}
	}
	return -1, 0, ""
}

// isVarRefByte reports whether c can appear inside a `$NAME` (no
// braces) reference. POSIX identifier chars only — relaxed forms are
// too ambiguous to recognize without braces.
func isVarRefByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_':
		return true
	}
	return false
}

// lookupVar resolves name against the file's already-parsed entries
// first, then any [WithExpansionParent] source. Cycles like
// `${A:-${A:-${A}}}` are bounded by [WithMaxExpansionDepth] and
// surface as an ExpansionError when the cap is reached.
func lookupVar(name string, opts parseOptions, resolved map[string]string) (string, bool) {
	if v, ok := resolved[name]; ok {
		return v, true
	}
	if opts.expansionParent != nil {
		if v, ok := opts.expansionParent.Lookup(name); ok {
			return v, true
		}
	}
	return "", false
}
