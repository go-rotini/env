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
	var (
		closes   braceTable // built by matchBraces on the first `${`
		built    bool
		pairsBuf [8]bracePair
	)
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
			if !built && i+1 < len(text) && text[i+1] == '{' {
				closes, built = matchBraces(text, pairsBuf[:0]), true
			}
			ref, n, ok := parseEnvRef(text, i, &closes)
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
		return nil, &assertionError{goPath: goPath, name: ref.name, msg: msg}
	}
	// envOpDash / envOpColonDash, default branch.
	def, err := expandText(ref.arg, goPath, lookup, append(chain, ref.name), depth+1)
	if err != nil {
		return nil, err
	}
	return append(b, def...), nil
}

// assertionError is a failed `${VAR:?message}`. The loader drops msg
// for secret fields, since it is expanded and may hold other values.
type assertionError struct {
	goPath, name, msg string
}

func (e *assertionError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("env: field %s: expansion of %q: %v", e.goPath, e.name, ErrExpansionAssertion)
	}
	return fmt.Sprintf("env: field %s: expansion of %q: %s: %v", e.goPath, e.name, e.msg, ErrExpansionAssertion)
}

func (e *assertionError) Unwrap() error { return ErrExpansionAssertion }

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

// parseEnvRef parses a `$NAME` or `${...}` reference at text[i]
// (caller guarantees text[i] == '$'). closes is the [matchBraces] table
// for text, needed only when text[i+1] is `{`. It returns the parsed
// reference, the number of bytes consumed, and ok=false when no
// recognizable reference starts there (a lone `$`, `$<non-identifier>`,
// or an unterminated `${`).
func parseEnvRef(text string, i int, closes *braceTable) (ref envRef, consumed int, ok bool) {
	s := text[i:]
	if len(s) < 2 {
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
	end := closes.closeOf(i + 1)
	if end < 0 {
		return envRef{}, 0, false
	}
	closeIdx := end - (i + 2)
	body := s[2 : 2+closeIdx]
	consumed = 2 + closeIdx + 1

	opIdx, opLen, op := firstTopLevelEnvOp(body)
	if opIdx < 0 {
		return envRef{name: body}, consumed, true
	}
	return envRef{name: body[:opIdx], op: op, arg: body[opIdx+opLen:]}, consumed, true
}

// braceTable pairs each `{` of a string with its matching `}`, in
// order of the `{` positions.
type braceTable struct {
	pairs []bracePair
	next  int // lookup cursor; closeOf queries arrive in increasing order
}

type bracePair struct {
	open, close int // close is -1 when the `{` is never closed
}

// closeOf returns the index of the `}` matching the `{` at pos, or -1
// when there is none. Successive calls must pass increasing positions.
func (t *braceTable) closeOf(pos int) int {
	for t.next < len(t.pairs) && t.pairs[t.next].open < pos {
		t.next++
	}
	if t.next < len(t.pairs) && t.pairs[t.next].open == pos {
		return t.pairs[t.next].close
	}
	return -1
}

// matchBraces pairs every `{` of s with its matching `}` (balancing
// nested braces), appending the pairs to buf. A backslash escapes the
// next byte. One pass serves every `${` in s, keeping expansion linear
// in len(s).
func matchBraces(s string, buf []bracePair) braceTable {
	t := braceTable{pairs: buf}
	var stackBuf [8]int
	open := stackBuf[:0] // indexes into t.pairs of unclosed braces
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			open = append(open, len(t.pairs))
			t.pairs = append(t.pairs, bracePair{open: i, close: -1})
		case '}':
			if n := len(open); n > 0 {
				t.pairs[open[n-1]].close = i
				open = open[:n-1]
			}
		}
	}
	return t
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
