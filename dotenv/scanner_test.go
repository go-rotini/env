package dotenv

import (
	"errors"
	"strings"
	"testing"
)

// scanFile is the lowest-level entry point; tests here exercise the
// tokenizer's handling of every quoting form, line ending, escape, and
// edge case.

func mustParse(t *testing.T, src string, opts ...ParseOption) *File {
	t.Helper()
	f, err := Parse([]byte(src), opts...)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return f
}

// --- empty / blank / comment lines ---

func TestScanner_Empty(t *testing.T) {
	f := mustParse(t, "")
	if len(f.entries) != 0 {
		t.Errorf("got %d entries, want 0", len(f.entries))
	}
}

func TestScanner_BlankLines(t *testing.T) {
	f := mustParse(t, "\n\n\n")
	if len(f.entries) != 3 {
		t.Errorf("got %d entries, want 3", len(f.entries))
	}
	for _, e := range f.entries {
		if e.Kind != EntryBlank {
			t.Errorf("got %v, want EntryBlank", e.Kind)
		}
	}
}

func TestScanner_CommentLines(t *testing.T) {
	f := mustParse(t, "# top-of-file\n#   indented comment\n")
	if len(f.entries) != 2 {
		t.Fatalf("got %d entries", len(f.entries))
	}
	for _, e := range f.entries {
		if e.Kind != EntryComment {
			t.Errorf("got %v, want EntryComment", e.Kind)
		}
	}
}

func TestScanner_LeadingWhitespaceComment(t *testing.T) {
	f := mustParse(t, "   # comment with leading spaces\n")
	if len(f.entries) != 1 || f.entries[0].Kind != EntryComment {
		t.Errorf("entries = %+v", f.entries)
	}
}

// --- bare assignments ---

func TestScanner_BareAssignment(t *testing.T) {
	f := mustParse(t, "FOO=bar\n")
	if len(f.entries) != 1 {
		t.Fatalf("got %d entries", len(f.entries))
	}
	e := f.entries[0]
	if e.Kind != EntryAssignment {
		t.Errorf("kind = %v", e.Kind)
	}
	if e.Key != "FOO" || e.Value != "bar" {
		t.Errorf("got (%q, %q)", e.Key, e.Value)
	}
}

func TestScanner_AssignmentWithoutLineEnding(t *testing.T) {
	f := mustParse(t, "FOO=bar")
	if len(f.entries) != 1 {
		t.Fatalf("got %d entries", len(f.entries))
	}
	if f.entries[0].Value != "bar" {
		t.Errorf("got %q", f.entries[0].Value)
	}
}

func TestScanner_EmptyValue(t *testing.T) {
	f := mustParse(t, "FOO=\n")
	if v, ok := f.Get("FOO"); !ok || v != "" {
		t.Errorf("got (%q, %v)", v, ok)
	}
}

func TestScanner_AssignmentWhitespaceAroundSep(t *testing.T) {
	f := mustParse(t, "FOO  =   bar\n")
	if v, _ := f.Get("FOO"); v != "bar" {
		t.Errorf("got %q", v)
	}
}

// --- export prefix ---

func TestScanner_ExportPrefix(t *testing.T) {
	f := mustParse(t, "export FOO=bar\n")
	if v, _ := f.Get("FOO"); v != "bar" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_ExportRejectedInStrictMode(t *testing.T) {
	_, err := Parse([]byte("export FOO=bar\n"), WithStrictDotenv())
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- YAML-style separator ---

func TestScanner_YAMLSeparator(t *testing.T) {
	f := mustParse(t, "FOO: bar\n")
	if v, _ := f.Get("FOO"); v != "bar" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_YAMLSeparatorRejectedInStrictMode(t *testing.T) {
	_, err := Parse([]byte("FOO: bar\n"), WithStrictDotenv())
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- single-quoted values ---

func TestScanner_SingleQuoted(t *testing.T) {
	f := mustParse(t, "FOO='hello world'\n")
	if v, _ := f.Get("FOO"); v != "hello world" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_SingleQuoted_NoEscape(t *testing.T) {
	f := mustParse(t, `FOO='literal $VAR \n value'`+"\n")
	if v, _ := f.Get("FOO"); v != `literal $VAR \n value` {
		t.Errorf("got %q", v)
	}
}

func TestScanner_SingleQuoted_Multiline(t *testing.T) {
	f := mustParse(t, "FOO='line1\nline2'\n")
	if v, _ := f.Get("FOO"); v != "line1\nline2" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_SingleQuoted_Unterminated(t *testing.T) {
	_, err := Parse([]byte("FOO='unterminated\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- double-quoted values ---

func TestScanner_DoubleQuoted_Plain(t *testing.T) {
	f := mustParse(t, `FOO="hello world"`+"\n")
	if v, _ := f.Get("FOO"); v != "hello world" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_DoubleQuoted_AllEscapes(t *testing.T) {
	src := `FOO="a\nb\tc\rd\be\ff\\g\$h\"i"` + "\n"
	f := mustParse(t, src)
	want := "a\nb\tc\rd\be\ff\\g$h\"i"
	if v, _ := f.Get("FOO"); v != want {
		t.Errorf("got %q\nwant %q", v, want)
	}
}

func TestScanner_DoubleQuoted_LineContinuation(t *testing.T) {
	src := "FOO=\"line1\\\nline2\"\n"
	f := mustParse(t, src)
	if v, _ := f.Get("FOO"); v != "line1line2" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_DoubleQuoted_LineContinuationCRLF(t *testing.T) {
	src := "FOO=\"line1\\\r\nline2\"\n"
	f := mustParse(t, src)
	if v, _ := f.Get("FOO"); v != "line1line2" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_DoubleQuoted_Multiline(t *testing.T) {
	src := "FOO=\"line one\nline two\nline three\"\n"
	f := mustParse(t, src)
	want := "line one\nline two\nline three"
	if v, _ := f.Get("FOO"); v != want {
		t.Errorf("got %q\nwant %q", v, want)
	}
}

func TestScanner_DoubleQuoted_UnknownEscapePassthrough(t *testing.T) {
	// Unknown escape: keep both bytes verbatim (motdotla-ish behavior).
	src := `FOO="a\zb"` + "\n"
	f := mustParse(t, src)
	if v, _ := f.Get("FOO"); v != `a\zb` {
		t.Errorf("got %q", v)
	}
}

func TestScanner_DoubleQuoted_Unterminated(t *testing.T) {
	_, err := Parse([]byte(`FOO="unterminated`))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- backtick-quoted values ---

func TestScanner_BacktickQuoted_RejectedByDefault(t *testing.T) {
	_, err := Parse([]byte("FOO=`hello`\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_BacktickQuoted_AcceptedWithOption(t *testing.T) {
	f := mustParse(t, "FOO=`literal $VAR`\n", WithBackticks())
	if v, _ := f.Get("FOO"); v != "literal $VAR" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_BacktickQuoted_Unterminated(t *testing.T) {
	_, err := Parse([]byte("FOO=`unterminated\n"), WithBackticks())
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- unquoted values ---

func TestScanner_Unquoted_TrimsTrailingWhitespace(t *testing.T) {
	f := mustParse(t, "FOO=bar   \n")
	if v, _ := f.Get("FOO"); v != "bar" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_Unquoted_NoEscapes(t *testing.T) {
	f := mustParse(t, `FOO=a\nb\tc`+"\n")
	if v, _ := f.Get("FOO"); v != `a\nb\tc` {
		t.Errorf("got %q", v)
	}
}

func TestScanner_Unquoted_TrailingComment(t *testing.T) {
	f := mustParse(t, "FOO=value # this is a comment\n")
	if v, _ := f.Get("FOO"); v != "value" {
		t.Errorf("got %q (want \"value\" without trailing comment)", v)
	}
}

func TestScanner_Unquoted_HashInsideValue(t *testing.T) {
	// `#` not preceded by whitespace is part of the value.
	f := mustParse(t, "FOO=val#part\n")
	if v, _ := f.Get("FOO"); v != "val#part" {
		t.Errorf("got %q", v)
	}
}

// --- line ending variants ---

func TestScanner_LFLineEndings(t *testing.T) {
	f := mustParse(t, "FOO=1\nBAR=2\n")
	if f.entries[0].Kind != EntryAssignment || f.entries[1].Kind != EntryAssignment {
		t.Errorf("entries: %+v", f.entries)
	}
}

func TestScanner_CRLFLineEndings(t *testing.T) {
	f := mustParse(t, "FOO=1\r\nBAR=2\r\n")
	if v, _ := f.Get("FOO"); v != "1" {
		t.Errorf("CRLF FOO = %q", v)
	}
	if v, _ := f.Get("BAR"); v != "2" {
		t.Errorf("CRLF BAR = %q", v)
	}
}

func TestScanner_BareCRLineEndings(t *testing.T) {
	f := mustParse(t, "FOO=1\rBAR=2\r")
	if v, _ := f.Get("FOO"); v != "1" {
		t.Errorf("bare-CR FOO = %q", v)
	}
	if v, _ := f.Get("BAR"); v != "2" {
		t.Errorf("bare-CR BAR = %q", v)
	}
}

// --- BOM ---

func TestScanner_StripUTF8BOM(t *testing.T) {
	src := append([]byte{0xEF, 0xBB, 0xBF}, []byte("FOO=1\n")...)
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("FOO"); v != "1" {
		t.Errorf("got %q (BOM not stripped)", v)
	}
}

// --- error positions ---

func TestScanner_InvalidIdentifier(t *testing.T) {
	_, err := Parse([]byte("123FOO=bar\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_MissingSeparator(t *testing.T) {
	_, err := Parse([]byte("FOO\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_UnexpectedAfterValue(t *testing.T) {
	// After a quoted value, only whitespace + optional comment + newline
	// is allowed.
	_, err := Parse([]byte(`FOO="hello"trailing`))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- relaxed names ---

func TestScanner_RelaxedNames(t *testing.T) {
	f := mustParse(t, "FOO-BAR=1\n", WithRelaxedNames())
	if v, _ := f.Get("FOO-BAR"); v != "1" {
		t.Errorf("got %q", v)
	}
}

func TestScanner_RelaxedNames_Rejected(t *testing.T) {
	_, err := Parse([]byte("FOO-BAR=1\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- DoS guards ---

func TestScanner_MaxFileSize(t *testing.T) {
	src := strings.Repeat("X", 100)
	_, err := Parse([]byte(src), WithMaxFileSize(50))
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestScanner_MaxFileSize_Disabled(t *testing.T) {
	src := strings.Repeat("X=1\n", 1000)
	_, err := Parse([]byte(src), WithMaxFileSize(0))
	if err != nil {
		t.Errorf("unexpected err with unlimited size: %v", err)
	}
}

func TestScanner_MaxLineLength(t *testing.T) {
	src := "FOO=" + strings.Repeat("x", 200) + "\n"
	_, err := Parse([]byte(src), WithMaxLineLength(50))
	if !errors.Is(err, ErrLineTooLong) {
		t.Errorf("err = %v, want ErrLineTooLong", err)
	}
}

// --- whitespace-only line ---

func TestScanner_WhitespaceOnlyLine(t *testing.T) {
	f := mustParse(t, "   \t  \n")
	if len(f.entries) != 1 || f.entries[0].Kind != EntryBlank {
		t.Errorf("whitespace-only line should be blank: %+v", f.entries)
	}
}

// --- identifier helpers ---

func TestIsIdentifierStart(t *testing.T) {
	cases := []struct {
		c       byte
		relaxed bool
		want    bool
	}{
		{'A', false, true},
		{'z', false, true},
		{'_', false, true},
		{'0', false, false},
		{'-', false, false},
		{'-', true, false},   // even relaxed: digits/punct can't START
		{0xC3, true, true},   // non-ASCII, relaxed
		{0xC3, false, false}, // non-ASCII, strict
	}
	for _, c := range cases {
		if got := isIdentifierStart(c.c, c.relaxed); got != c.want {
			t.Errorf("isIdentifierStart(%q, relaxed=%v) = %v, want %v",
				c.c, c.relaxed, got, c.want)
		}
	}
}

func TestIsIdentifierByte(t *testing.T) {
	cases := []struct {
		c       byte
		relaxed bool
		want    bool
	}{
		{'A', false, true},
		{'9', false, true},
		{'_', false, true},
		{'-', false, false},
		{'-', true, true},
		{'.', true, true},
		{'.', false, false},
		{'!', false, false},
		{0xC3, true, true},
		{0xC3, false, false},
	}
	for _, c := range cases {
		if got := isIdentifierByte(c.c, c.relaxed); got != c.want {
			t.Errorf("isIdentifierByte(%q, relaxed=%v) = %v, want %v",
				c.c, c.relaxed, got, c.want)
		}
	}
}

// --- export prefix edge cases ---

func TestScanner_ExportShortLine(t *testing.T) {
	// "export" without trailing space + identifier is just an identifier.
	f := mustParse(t, "export=value\n")
	if v, _ := f.Get("export"); v != "value" {
		t.Errorf("got %q", v)
	}
}

// --- single-line input without trailing newline ---

func TestScanner_NoTrailingNewline_Blank(t *testing.T) {
	// An empty file produces no entries.
	f := mustParse(t, "")
	if len(f.entries) != 0 {
		t.Errorf("got %d entries", len(f.entries))
	}
}

func TestScanner_NoTrailingNewline_Comment(t *testing.T) {
	f := mustParse(t, "# comment without newline")
	if len(f.entries) != 1 || f.entries[0].Kind != EntryComment {
		t.Errorf("got %+v", f.entries)
	}
}

// --- missing key after export ---

func TestScanner_ExportPlusInvalidIdent(t *testing.T) {
	// "export 123" → not an identifier; export prefix not consumed.
	// Falls back to treating "export 123=foo" as a key starting with "e".
	// Actually the scanner sees "export 123=foo" — checks hasExportPrefix.
	// hasExportPrefix returns false because the byte after "export " is
	// '1' which fails isIdentifierStart. So scanner treats whole line
	// as starting with "export" → tries to parse "export" as key, then
	// space, then expects '='. "export 123" → key="export", separator
	// expected after whitespace, sees '1' → error.
	_, err := Parse([]byte("export 123=foo\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- additional edge cases ---

func TestScanner_HashImmediatelyAfterEquals(t *testing.T) {
	// `FOO=#comment` — no whitespace before #, but # at start of value.
	// Per scanUnquoted logic, # at the very start (s.pos == start) is
	// treated as a comment introducer.
	f := mustParse(t, "FOO=#comment-only\n")
	if v, _ := f.Get("FOO"); v != "" {
		t.Errorf("got %q (want empty value when # immediately follows =)", v)
	}
}

func TestScanner_UnexpectedByteAfterQuotedValue(t *testing.T) {
	_, err := Parse([]byte(`FOO="hello"X` + "\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_DoubleQuoted_TerminatedAtEOF(t *testing.T) {
	// Trailing unterminated escape at end of input.
	_, err := Parse([]byte(`FOO="abc\`))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_OnlyExportNoIdent(t *testing.T) {
	// "export " followed by nothing parseable as a key.
	_, err := Parse([]byte("export \n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_MissingKeyOnly(t *testing.T) {
	// "=value" — no key.
	_, err := Parse([]byte("=value\n"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_KeyOnlyNoSepEOF(t *testing.T) {
	// "FOO" with no separator at end of file.
	_, err := Parse([]byte("FOO"))
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestScanner_HasExportPrefix_EdgeCases(t *testing.T) {
	if hasExportPrefix([]byte("export ")) {
		t.Error("'export ' alone (no following ident) should not match")
	}
	if !hasExportPrefix([]byte("export X")) {
		t.Error("'export X' should match")
	}
	if hasExportPrefix([]byte("exp")) {
		t.Error("short prefix should not match")
	}
}
