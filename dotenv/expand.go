package dotenv

import (
	"fmt"
	"strings"

	"github.com/go-rotini/env"
)

// dollarSuppressSentinel is the byte the scanner emits in front of a
// literal `$` that came from a `\$` escape in a double-quoted value.
// The expander treats sentinel-marked `$` as a literal (no expansion)
// and strips the sentinel from the final value.
//
// The byte is U+0007 (BELL); env values practically never contain it,
// and any user-supplied BELL byte in a literal value passes through
// unaffected (it doesn't precede a `$`).
const dollarSuppressSentinel = 0x07

// expandAll runs variable expansion over every assignment entry's
// decoded value, in source order. Lookups consult the file's own
// already-resolved entries first, then any parent supplied via
// [WithExpansionParent].
//
// Single-quoted and backtick-quoted entries are skipped entirely (per
// dialect convention, those forms suppress expansion). Double-quoted
// values still need their sentinel-marked dollars resolved even when
// expansion is disabled, so the scanner's `\$` → sentinel marking is
// always undone here.
//
// On the first error encountered, expansion halts and the
// [*ExpansionError] is returned.
func (f *File) expandAll(opts parseOptions) error {
	resolved := make(map[string]string)
	for i := range f.entries {
		e := &f.entries[i]
		if e.Kind != EntryAssignment {
			continue
		}
		// Single-quoted and backtick: literal values, no expansion.
		// Their decoded Value already contains the source bytes verbatim.
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

// expand evaluates `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR-default}`,
// and `${VAR:?error}` references in s. Backslash-dollar (`\$`) suppresses
// expansion of the following identifier.
//
// resolved maps names already resolved in the current file (consulted
// first); the parent Source from opts is consulted second.
func expand(s string, pos env.Position, opts parseOptions, resolved map[string]string) (string, error) {
	return expandWithDepth(s, pos, opts, resolved, 0)
}

// expandWithDepth is the recursive workhorse. depth is the current
// recursion level; the caller's [WithMaxExpansionDepth] cap protects
// against runaway nested default expansion.
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
			// Scanner-inserted marker for `\$` in double-quoted values.
			// Emit a literal `$` and skip the sentinel + the dollar.
			if i+1 < len(s) && s[i+1] == '$' {
				b.WriteByte('$')
				i += 2
				continue
			}
			// Defensive: stray sentinel without a following `$` is
			// dropped silently (should never happen in practice).
			i++
		case '\\':
			// `\$` in unquoted values isn't a recognized dialect escape
			// (unquoted values normally have no escape interpretation),
			// but accept it here as a courtesy: if WithExpand is on, we
			// treat `\$` as suppressing expansion of the following
			// identifier.
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
				// Lone `$` or `$<non-ident>`: emit verbatim.
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

// varRef captures the parsed shape of one `$VAR` / `${...}` reference.
type varRef struct {
	name           string
	defaultVal     string
	assertVal      string
	defaultIfEmpty bool
	hasDefault     bool
	hasAssert      bool
	consumed       int
}

// parseVarRef parses one `$VAR` / `${...}` reference at the start of s.
// Returns the parsed [varRef] and ok=true on success; ok=false when s
// does not begin with a recognizable reference (lone `$`, `$<non-ident>`,
// unterminated `${`).
//
// Recognized shapes:
//
//	$NAME
//	${NAME}
//	${NAME:-DEFAULT}     hasDefault=true, defaultIfEmpty=true
//	${NAME-DEFAULT}      hasDefault=true, defaultIfEmpty=false
//	${NAME:?MSG}         hasAssert=true
func parseVarRef(s string) (varRef, bool) {
	if len(s) < 2 || s[0] != '$' {
		return varRef{}, false
	}
	if s[1] != '{' {
		// $NAME form.
		end := 1
		for end < len(s) && isVarRefByte(s[end]) {
			end++
		}
		if end == 1 {
			return varRef{}, false
		}
		return varRef{name: s[1:end], consumed: end}, true
	}
	// ${...} form. Find the matching `}`, balancing nested `${...}`.
	closeIdx := findMatchingBrace(s[2:])
	if closeIdx < 0 {
		return varRef{}, false
	}
	body := s[2 : 2+closeIdx]
	consumed := 2 + closeIdx + 1

	// Find the operator: ":-", "-", or ":?" — first occurrence at the
	// TOP LEVEL of body (i.e., not inside a nested ${...}).
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

// findMatchingBrace returns the index in s of the `}` that matches the
// `{` that was just opened (i.e., the outer scope's). Nested `${...}`
// are balanced. Returns -1 when no matching brace is found.
func findMatchingBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			// Skip the escaped character.
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

// firstTopLevelOperator finds the first ":-", ":?", or "-" operator in
// body that appears at the top level (not inside a nested ${...}).
// Returns (index, length, op) or (-1, 0, "") if none found.
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

// isVarRefByte reports whether c can appear inside a `$NAME` (no-brace)
// reference. POSIX identifier chars only — no relaxed-name extensions
// (the relaxed forms are too ambiguous to recognize without braces).
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
// first, then any parent Source from opts. Returns the value and whether
// the name was found in either source.
//
// Runaway recursion via deeply-nested defaults is bounded by
// [WithMaxExpansionDepth]; cycles in the literal source text (e.g.,
// `${A:-${A:-${A}}}`) hit the depth limit and surface as a
// max-depth ExpansionError.
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
