package dotenv

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-rotini/env"
)

// scanner is a single-pass byte-level tokenizer over a `.env` source.
// It produces logical entries (blank / comment / assignment) in source
// order, preserving each entry's raw bytes for round-trip emission.
type scanner struct {
	src  []byte
	pos  int
	line int
	col  int
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

func (s *scanner) done() bool {
	return s.pos >= len(s.src)
}

// peek returns the byte at the current position, or 0 at EOF.
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

func (s *scanner) advance(n int) {
	for i := 0; i < n && s.pos < len(s.src); i++ {
		c := s.src[s.pos]
		s.pos++
		switch c {
		case '\n':
			s.line++
			s.col = 1
		case '\r':
			// Don't double-count CRLF: an LF following CR will bump
			// the line itself on the next iteration.
			if s.pos < len(s.src) && s.src[s.pos] == '\n' {
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

func (s *scanner) currentPos() env.Position {
	return env.Position{
		File:   s.opts.filePath,
		Line:   s.line,
		Column: s.col,
		Offset: s.pos,
	}
}

// scanFile drains src into a *File. Returns the first parse error.
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

// scanLogicalEntry reads one logical entry from the current position.
// On return the scanner is positioned past the terminating line ending.
func (s *scanner) scanLogicalEntry() (Entry, error) {
	physStart := s.pos

	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}

	if s.done() || isLineEnding(s.peek()) {
		s.consumeLineEnding()
		if err := s.checkLineLength(physStart); err != nil {
			return Entry{}, err
		}
		return Entry{Kind: EntryBlank}, nil
	}

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

	return s.scanAssignment(physStart)
}

func (s *scanner) scanAssignment(physStart int) (Entry, error) {
	startPos := s.currentPos()

	if hasExportPrefix(s.src[s.pos:]) {
		if s.opts.strictDotenv {
			return Entry{}, &ParseError{Pos: startPos, Message: "export prefix not allowed in strict mode"}
		}
		s.advance(len("export "))
		for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
			s.advance(1)
		}
	}

	keyStart := s.pos
	if !isIdentifierStart(s.peek(), s.opts.relaxedNames) {
		return Entry{}, &ParseError{Pos: s.currentPos(), Message: fmt.Sprintf("invalid identifier start byte %q", s.peek())}
	}
	s.advance(1)
	for !s.done() && isIdentifierByte(s.peek(), s.opts.relaxedNames) {
		s.advance(1)
	}
	key := string(s.src[keyStart:s.pos])

	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}

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

	for !s.done() && (s.peek() == ' ' || s.peek() == '\t') {
		s.advance(1)
	}

	value, quote, err := s.scanValue()
	if err != nil {
		return Entry{}, err
	}

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

// scanValue reads an assignment's value, dispatching by quote kind.
// On return the scanner is positioned after the value (or its closing
// quote) but before any trailing whitespace / comment / line ending.
// Returns the decoded value, the quote form (0, '"', '\”, '`'), and
// any parse error.
func (s *scanner) scanValue() (string, byte, error) {
	if s.done() || isLineEnding(s.peek()) {
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

// scanUnquoted reads up to end-of-line or the start of a `# comment`,
// trimming trailing whitespace. `#` only introduces a comment when
// preceded by whitespace, so `foo#bar` is one value.
func (s *scanner) scanUnquoted() string {
	start := s.pos
	for !s.done() && !isLineEnding(s.peek()) {
		c := s.peek()
		if c == '#' {
			if s.pos > start {
				prev := s.src[s.pos-1]
				if prev == ' ' || prev == '\t' {
					break
				}
			} else {
				break
			}
		}
		s.advance(1)
	}
	raw := string(s.src[start:s.pos])
	return strings.TrimRight(raw, " \t")
}

// scanSingleQuoted reads a single-quoted value: bytes are literal, no
// escapes, multi-line allowed.
func (s *scanner) scanSingleQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1)
	start := s.pos
	for !s.done() && s.peek() != '\'' {
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated single-quoted value"}
	}
	value := string(s.src[start:s.pos])
	s.advance(1)
	return value, nil
}

// scanBacktickQuoted is the backtick equivalent of [scanSingleQuoted];
// only reachable under [WithBackticks].
func (s *scanner) scanBacktickQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1)
	start := s.pos
	for !s.done() && s.peek() != '`' {
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated backtick-quoted value"}
	}
	value := string(s.src[start:s.pos])
	s.advance(1)
	return value, nil
}

// scanDoubleQuoted reads a double-quoted value with the standard
// escape table. Literal newlines are permitted inside the quotes.
// `\<LF>` / `\<CRLF>` is line continuation; `\$` is preserved via
// [dollarSuppressSentinel] when expansion is enabled. Unknown escapes
// pass through as `\X` (matching motdotla's tolerant behavior).
func (s *scanner) scanDoubleQuoted() (string, error) {
	openPos := s.currentPos()
	s.advance(1)
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
				if s.opts.expand {
					buf.WriteByte(dollarSuppressSentinel)
				}
				buf.WriteByte('$')
				s.advance(2)
			case '\n':
				s.advance(2)
			case '\r':
				s.advance(2)
				if !s.done() && s.peek() == '\n' {
					s.advance(1)
				}
			case 0:
				return "", &ParseError{Pos: s.currentPos(), Message: "unterminated escape sequence"}
			default:
				buf.WriteByte('\\')
				buf.WriteByte(next)
				s.advance(2)
			}
			continue
		}
		buf.WriteByte(c)
		s.advance(1)
	}
	if s.done() {
		return "", &ParseError{Pos: openPos, Message: "unterminated double-quoted value"}
	}
	s.advance(1)
	return buf.String(), nil
}

// consumeTrailingAndLineEnding skips whitespace + optional `# comment`
// + the line ending after a value. Errors when a non-whitespace,
// non-`#` byte appears between the value and end-of-line.
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

// consumeLineEnding reads one line ending (LF, CRLF, or bare CR).
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

// checkLineLength enforces [WithMaxLineLength] against the physical
// line starting at physStart. Multi-line values aren't measured in
// aggregate; the leading physical line is the bound.
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

// hasExportPrefix reports whether b begins with "export " followed by
// an identifier-start byte.
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

// isIdentifierStart reports whether c may begin a POSIX identifier.
// With relaxed=true, non-ASCII bytes are also accepted; even in
// relaxed mode an identifier cannot start with a digit.
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
		return true
	}
	return false
}

// isIdentifierByte reports whether c may appear after the first byte
// of an identifier. relaxed=true also permits hyphens, dots, and
// non-ASCII bytes.
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
