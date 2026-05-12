package env

import "fmt"

// maxExpandDepth bounds recursion through the `default` / `message`
// parts of `${VAR-default}` / `${VAR:-default}` / `${VAR:?message}`.
// Plain `${VAR}` references are resolved in a single forward pass and
// never re-expanded, so this is the only place expansion can recurse.
const maxExpandDepth = 16

// expandFieldValue resolves variable references in text for the
// `expand` tag option. Recognized forms:
//
//	$VAR             — VAR's source value, or "" if unset
//	${VAR}           — same
//	${VAR-default}   — default if VAR is unset
//	${VAR:-default}  — default if VAR is unset or set to ""
//	${VAR:?message}  — error (wrapping [ErrExpansionAssertion]) if VAR is unset
//
// `\$` produces a literal `$`. References are looked up via lookup,
// which is the loader's composed source — the name is used verbatim
// (no [WithPrefix] is applied), and only the source chain is consulted
// (not other fields' default= values). The default / message parts are
// themselves expanded; non-termination there yields a [*CycleError].
// goPath is included in returned errors for field context.
func expandFieldValue(text, goPath string, lookup func(name string) (value string, ok bool)) (string, error) {
	return expandText(text, goPath, lookup, nil, 0)
}

func expandText(text, goPath string, lookup func(string) (string, bool), chain []string, depth int) (string, error) {
	if depth > maxExpandDepth {
		return "", &CycleError{Chain: append([]string(nil), chain...)}
	}
	var b []byte
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case '\\':
			if i+1 < len(text) && text[i+1] == '$' {
				b = append(b, '$')
				i += 2
				continue
			}
			b = append(b, c)
			i++
		case '$':
			ref, n, ok := parseEnvRef(text[i:])
			if !ok {
				b = append(b, '$')
				i++
				continue
			}
			var err error
			if b, err = appendEnvRef(b, ref, goPath, lookup, chain, depth); err != nil {
				return "", err
			}
			i += n
		default:
			b = append(b, c)
			i++
		}
	}
	return string(b), nil
}

// appendEnvRef resolves one reference against lookup and appends the
// result (or the expanded default) to b. A `${VAR:?msg}` for an unset
// VAR returns an error wrapping [ErrExpansionAssertion].
func appendEnvRef(b []byte, ref envRef, goPath string, lookup func(string) (string, bool), chain []string, depth int) ([]byte, error) {
	val, isSet := lookup(ref.name)
	switch ref.op {
	case envOpNone:
		if isSet {
			b = append(b, val...)
		}
		return b, nil
	case envOpDash:
		if isSet {
			return append(b, val...), nil
		}
	case envOpColonDash:
		if isSet && val != "" {
			return append(b, val...), nil
		}
	case envOpColonQuestion:
		if isSet {
			return append(b, val...), nil
		}
		msg, err := expandText(ref.arg, goPath, lookup, append(chain, ref.name), depth+1)
		if err != nil {
			return nil, err
		}
		if msg == "" {
			msg = "parameter not set"
		}
		return nil, fmt.Errorf("env: field %s: expansion of %q: %s: %w", goPath, ref.name, msg, ErrExpansionAssertion)
	}
	// envOpDash / envOpColonDash, default branch.
	def, err := expandText(ref.arg, goPath, lookup, append(chain, ref.name), depth+1)
	if err != nil {
		return nil, err
	}
	return append(b, def...), nil
}

type envRefOp int

const (
	envOpNone envRefOp = iota
	envOpDash
	envOpColonDash
	envOpColonQuestion
)

type envRef struct {
	name string
	op   envRefOp
	arg  string // default / message text; only meaningful when op != envOpNone
}

// parseEnvRef parses a `$NAME` or `${...}` reference at the start of s
// (caller guarantees s[0] == '$'). It returns the parsed reference, the
// number of bytes consumed, and ok=false when s does not begin with a
// recognizable reference (a lone `$`, `$<non-identifier>`, or an
// unterminated `${`).
func parseEnvRef(s string) (ref envRef, consumed int, ok bool) {
	if len(s) < 2 || s[0] != '$' {
		return envRef{}, 0, false
	}
	if s[1] != '{' {
		end := 1
		for end < len(s) && isEnvRefByte(s[end]) {
			end++
		}
		if end == 1 {
			return envRef{}, 0, false
		}
		return envRef{name: s[1:end]}, end, true
	}
	closeIdx := matchBrace(s[2:])
	if closeIdx < 0 {
		return envRef{}, 0, false
	}
	body := s[2 : 2+closeIdx]
	consumed = 2 + closeIdx + 1

	opIdx, opLen, op := firstTopLevelEnvOp(body)
	if opIdx < 0 {
		return envRef{name: body}, consumed, true
	}
	return envRef{name: body[:opIdx], op: op, arg: body[opIdx+opLen:]}, consumed, true
}

// matchBrace returns the index in s of the `}` matching the implicit
// outer `{` (balancing nested `${...}`), or -1 when none. A backslash
// escapes the next byte.
func matchBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
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

// firstTopLevelEnvOp finds the first ":-", ":?", or "-" at the top
// level of a `${...}` body (not inside a nested `${...}`). It returns
// (-1, 0, envOpNone) when none is present.
func firstTopLevelEnvOp(body string) (idx, length int, op envRefOp) {
	depth := 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '\\':
			i++
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
					return i, 2, envOpColonDash
				case '?':
					return i, 2, envOpColonQuestion
				}
			}
		case '-':
			if depth == 0 {
				return i, 1, envOpDash
			}
		}
	}
	return -1, 0, envOpNone
}

// isEnvRefByte reports whether c may appear in a brace-less `$NAME`
// reference (POSIX identifier bytes only — relaxed forms are too
// ambiguous to recognize without braces).
func isEnvRefByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		return true
	}
	return false
}
