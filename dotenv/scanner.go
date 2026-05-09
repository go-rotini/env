package dotenv

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-rotini/env"
)

// scanner is a single-pass byte-level tokenizer over a `.env` source.
//
// It produces logical entries (blank, comment, assignment) in source
// order, preserving each entry's verbatim raw bytes for round-trip and
// decoding the assignment value through the appropriate quoting rules.
type scanner struct {
	src  []byte
	pos  int // current byte offset into src
	line int // 1-based current line (advanced as newlines are consumed)
	col  int // 1-based current column
	opts parseOptions
}

func newScanner(src []byte, opts parseOptions) *scanner {
	return &scanner{
		src:  src,
		line: 1,
		col:  1,
		opts: opts,
	}
}

// done reports whether the scanner has consumed all input.
func (s *scanner) done() bool {
	return s.pos >= len(s.src)
}

// next returns the byte at the current position without advancing, or
// 0 when at end of input.
func (s *scanner) peek() byte {
	if s.pos >= len(s.src) {
		return 0
	}
	return s.src[s.pos]
}

func (s *scanner) peekAt(offset int) byte {
	if s.pos+offset >= len(s.src) {
		return 0
	}
	return s.src[s.pos+offset]
}

// advance moves the scanner past n bytes, updating the line/column
// counters as it goes.
func (s *scanner) advance(n int) {
	for i := 0; i < n && s.pos < len(s.src); i++ {
		c := s.src[s.pos]
		s.pos++
		switch c {
		case '\n':
			s.line++
			s.col = 1
		case '\r':
			// CR-only line ending: bump line. CR-LF is handled by the
			// LF below incrementing line again? No — we need to NOT
			// double-count. Handle CR specially: if next is LF, it's a
			// CRLF; the LF advance increments line. So plain CR with no
			// following LF is a line ending alone.
			if s.pos < len(s.src) && s.src[s.pos] == '\n' {
				// CRLF: don't bump on CR; let LF do it on the next iteration.
				s.col = 1
			} else {
				s.line++
				s.col = 1
			}
		default:
			s.col++
		}
	}
}

// currentPos snapshots the current line/column with the optional file path.
func (s *scanner) currentPos() env.Position {
	return env.Position{
		File:   s.opts.filePath,
		Line:   s.line,
		Column: s.col,
		Offset: s.pos,
	}
}

// scanFile drains the source into a *File. Returns the first ParseError
// encountered.
func scanFile(src []byte, opts parseOptions) (*File, error) {
	if opts.maxFileSize > 0 && int64(len(src)) > opts.maxFileSize {
		return nil, fmt.Errorf("size %d > %d: %w", len(src), opts.maxFileSize, ErrFileTooLarge)
	}
	src = stripUTF8BOM(src)

	s := newScanner(src, opts)
	f := &File{path: opts.filePath}

	for !s.done() {
		startPos := s.currentPos()
		startOff := s.pos

		// Track the physical line's length to enforce maxLineLength.
		// A multi-line double-quoted value resets line counting at
		// each LF so each physical line is bounded independently.
		entry, err := s.scanLogicalEntry()
		if err != nil {
			return nil, err
		}
		entry.Pos = startPos
		entry.raw = bytes.Clone(src[startOff:s.pos])
		f.entries = append(f.entries, entry)
	}
	return f, nil
}

// stripUTF8BOM removes a leading UTF-8 BOM (EF BB BF) if present.
func stripUTF8BOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// scanLogicalEntry reads one logical entry (blank, comment, or
// assignment) from the current position. The scanner is positioned
// immediately after the entry's terminating line ending on return.
func (s *scanner) scanLogicalEntry() (Entry, error) {
	// Track length of the leading physical line for enforcement.
	physStart := s.pos

	// Skip leading whitespace (spaces and tabs).
	wsStart := s.pos
	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}
	leadingWS := s.src[wsStart:s.pos]

	// Blank line: only whitespace before the line ending.
	if s.done() || isLineEnding(s.peek()) {
		s.consumeLineEnding()
		if err := s.checkLineLength(physStart); err != nil {
			return Entry{}, err
		}
		_ = leadingWS
		return Entry{Kind: EntryBlank}, nil
	}

	// Comment line.
	if s.peek() == '#' {
		for !s.done() && !isLineEnding(s.peek()) {
			s.advance(1)
		}
		s.consumeLineEnding()
		if err := s.checkLineLength(physStart); err != nil {
			return Entry{}, err
		}
		return Entry{Kind: EntryComment}, nil
	}

	// Assignment.
	return s.scanAssignment(physStart)
}

// scanAssignment parses one KEY[=:]VALUE line.
func (s *scanner) scanAssignment(physStart int) (Entry, error) {
	startPos := s.currentPos()

	// Optional leading "export " (Bash-style).
	if hasExportPrefix(s.src[s.pos:]) {
		if s.opts.strictDotenv {
			return Entry{}, &ParseError{Pos: startPos, Message: "export prefix not allowed in strict mode"}
		}
		s.advance(len("export "))
		// Skip any extra whitespace after `export `.
		for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
			s.advance(1)
		}
	}

	// Identifier.
	keyStart := s.pos
	if !isIdentifierStart(s.peek(), s.opts.relaxedNames) {
		return Entry{}, &ParseError{Pos: s.currentPos(), Message: fmt.Sprintf("invalid identifier start byte %q", s.peek())}
	}
	s.advance(1)
	for !s.done() && isIdentifierByte(s.peek(), s.opts.relaxedNames) {
		s.advance(1)
	}
	key := string(s.src[keyStart:s.pos])

	// Whitespace before separator.
	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}

	// Separator: '=' (canonical) or ':' (YAML-style; rejected in strict mode).
	if s.done() {
		return Entry{}, &ParseError{Pos: s.currentPos(), Message: "expected '=' or ':' after key"}
	}
	switch s.peek() {
	case '=':
		s.advance(1)
	case ':':
		if s.opts.strictDotenv {
			return Entry{}, &ParseError{Pos: s.currentPos(), Message: "YAML-style ':' separator not allowed in strict mode"}
		}
		s.advance(1)
	default:
		return Entry{}, &ParseError{Pos: s.currentPos(), Message: fmt.Sprintf("expected '=' or ':' after key, got %q", s.peek())}
	}

	// Whitespace before value.
	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}

	// Value.
	value, quote, err := s.scanValue()
	if err != nil {
		return Entry{}, err
	}

	// Trailing whitespace and optional `# comment` are consumed by
	// scanValue's caller. Then the line ending.
	if err := s.consumeTrailingAndLineEnding(); err != nil {
		return Entry{}, err
	}

	if err := s.checkLineLength(physStart); err != nil {
		return Entry{}, err
	}

	return Entry{
		Kind:  EntryAssignment,
		Key:   key,
		Value: value,
		quote: quote,
	}, nil
}

// scanValue reads the value part of an assignment, dispatching by quote
// kind. On return the scanner is positioned after the value's last byte
// (or after the closing quote for quoted forms), but BEFORE any trailing
// whitespace, trailing comment, or line ending.
//
// Returns the decoded value, the source quoting form (0/`"`/`'`/“ ` “),
// and any parse error.
func (s *scanner) scanValue() (string, byte, error) {
	if s.done() || isLineEnding(s.peek()) {
		// Empty value: KEY=  (or KEY= EOL).
		return "", 0, nil
	}
	switch s.peek() {
	case '"':
		v, err := s.scanDoubleQuoted()
		return v, '"', err
	case '\'':
		v, err := s.scanSingleQuoted()
		return v, '\'', err
	case '`':
		if s.opts.backticks {
			v, err := s.scanBacktickQuoted()
			return v, '`', err
		}
		return "", 0, &ParseError{Pos: s.currentPos(), Message: "backtick-quoted value not allowed; use WithBackticks() to enable"}
	default:
		return s.scanUnquoted(), 0, nil
	}
}

// scanUnquoted reads an unquoted value: from current position to either
// end-of-line or the start of a `# comment`. Trailing whitespace is
// trimmed from the returned value.
//
// A `#` introduces a comment ONLY when preceded by whitespace; `foo#bar`
// is a single value, not a value followed by a comment.
func (s *scanner) scanUnquoted() string {
	start := s.pos
	for !s.done() && !isLineEnding(s.peek()) {
		c := s.peek()
		if c == '#' {
			// Hash starts a trailing comment only if it's separated from
			// the value by whitespace. Look back at the previous byte.
			if s.pos > start {
				prev := s.src[s.pos-1]
				if prev == ' ' || prev == '\t' {
					break
				}
			} else {
				// `#` immediately after the separator; treat as comment.
				break
			}
		}
		s.advance(1)
	}
	raw := string(s.src[start:s.pos])
	return strings.TrimRight(raw, " \t")
}

// scanSingleQuoted reads a single-quoted value: every byte except `'`
// is literal. No escape sequences. Multi-line allowed (literal newlines
// preserved).
func (s *scanner) scanSingleQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1) // consume opening quote
	start := s.pos
	for !s.done() && s.peek() != '\'' {
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated single-quoted value"}
	}
	value := string(s.src[start:s.pos])
	s.advance(1) // consume closing quote
	return value, nil
}

// scanBacktickQuoted reads a backtick-quoted value: identical to single-
// quoted (no expansion, no escapes). Only callable when [WithBackticks]
// is enabled.
func (s *scanner) scanBacktickQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1) // consume opening quote
	start := s.pos
	for !s.done() && s.peek() != '`' {
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated backtick-quoted value"}
	}
	value := string(s.src[start:s.pos])
	s.advance(1) // consume closing quote
	return value, nil
}

// scanDoubleQuoted reads a double-quoted value with the standard escape
// table. Multi-line values are permitted; literal newlines inside the
// quotes appear in the decoded value verbatim.
func (s *scanner) scanDoubleQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1) // consume opening quote
	var buf strings.Builder
	for !s.done() && s.peek() != '"' {
		c := s.peek()
		if c == '\\' {
			next := s.peekAt(1)
			switch next {
			case 'n':
				buf.WriteByte('\n')
				s.advance(2)
			case 'r':
				buf.WriteByte('\r')
				s.advance(2)
			case 't':
				buf.WriteByte('\t')
				s.advance(2)
			case 'b':
				buf.WriteByte('\b')
				s.advance(2)
			case 'f':
				buf.WriteByte('\f')
				s.advance(2)
			case '"':
				buf.WriteByte('"')
				s.advance(2)
			case '\\':
				buf.WriteByte('\\')
				s.advance(2)
			case '$':
				// When expansion will run, emit a sentinel so the
				// post-pass expander knows this `$` came from `\$`
				// and must not trigger expansion. When expansion is
				// off, just emit a literal `$` directly.
				if s.opts.expand {
					buf.WriteByte(dollarSuppressSentinel)
				}
				buf.WriteByte('$')
				s.advance(2)
			case '\n':
				// Line continuation: consume \\<LF>, emit nothing.
				s.advance(2)
			case '\r':
				// `\<CR>` or `\<CRLF>` — line continuation.
				s.advance(2)
				if !s.done() && s.peek() == '\n' {
					s.advance(1)
				}
			case 0:
				return "", &ParseError{Pos: s.currentPos(), Message: "unterminated escape sequence"}
			default:
				// Unknown escape: keep the backslash and the next byte
				// verbatim. This matches motdotla/dotenv's tolerant
				// behavior. godotenv would reject; we accept by default.
				buf.WriteByte('\\')
				buf.WriteByte(next)
				s.advance(2)
			}
			continue
		}
		// Literal byte (including LF / CR for multi-line strings).
		buf.WriteByte(c)
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated double-quoted value"}
	}
	s.advance(1) // consume closing quote
	return buf.String(), nil
}

// consumeTrailingAndLineEnding skips whitespace + an optional `# comment`
// + the line ending after a value. Errors if any non-whitespace, non-`#`
// byte appears between the value and the end-of-line.
func (s *scanner) consumeTrailingAndLineEnding() error {
	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}
	if s.done() {
		return nil
	}
	if isLineEnding(s.peek()) {
		s.consumeLineEnding()
		return nil
	}
	if s.peek() == '#' {
		for !s.done() && !isLineEnding(s.peek()) {
			s.advance(1)
		}
		s.consumeLineEnding()
		return nil
	}
	return &ParseError{
		Pos:     s.currentPos(),
		Message: fmt.Sprintf("unexpected byte %q after value", s.peek()),
	}
}

// consumeLineEnding reads one line ending: LF, CRLF, or bare CR.
func (s *scanner) consumeLineEnding() {
	if s.done() {
		return
	}
	switch s.peek() {
	case '\n':
		s.advance(1)
	case '\r':
		s.advance(1)
		if !s.done() && s.peek() == '\n' {
			s.advance(1)
		}
	}
}

// checkLineLength enforces WithMaxLineLength against the physical line
// starting at physStart and ending at the current position. Multi-line
// double-quoted values that span multiple physical lines aren't measured
// in aggregate; this function is called once per logical entry and uses
// the entry's leading physical line as the bound.
func (s *scanner) checkLineLength(physStart int) error {
	if s.opts.maxLineLength <= 0 {
		return nil
	}
	if s.pos-physStart > s.opts.maxLineLength {
		return fmt.Errorf("line at offset %d exceeds %d bytes: %w",
			physStart, s.opts.maxLineLength, ErrLineTooLong)
	}
	return nil
}

// hasExportPrefix reports whether b begins with "export " followed by an
// identifier-start byte.
func hasExportPrefix(b []byte) bool {
	const prefix = "export "
	if !bytes.HasPrefix(b, []byte(prefix)) {
		return false
	}
	if len(b) <= len(prefix) {
		return false
	}
	return isIdentifierStart(b[len(prefix)], false)
}

// isLineEnding reports whether c is LF or CR.
func isLineEnding(c byte) bool {
	return c == '\n' || c == '\r'
}

// isIdentifierStart reports whether c can begin a POSIX identifier.
// With relaxed=true, additional characters (`-`, `.`, ASCII digit) are
// permitted — note that even relaxed names cannot START with a digit
// to avoid confusion with shell scripting that interprets numeric
// prefixes as integer literals.
func isIdentifierStart(c byte, relaxed bool) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c == '_':
		return true
	}
	if relaxed && c >= 0x80 {
		return true // non-ASCII byte
	}
	return false
}

// isIdentifierByte reports whether c can appear inside a POSIX identifier
// after the first byte. With relaxed=true, hyphens, dots, and non-ASCII
// bytes are also permitted.
func isIdentifierByte(c byte, relaxed bool) bool {
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
	if relaxed {
		switch c {
		case '-', '.':
			return true
		}
		if c >= 0x80 {
			return true
		}
	}
	return false
}
