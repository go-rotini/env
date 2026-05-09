package dotenv

import (
	"bytes"
	"strings"
)

// Marshal returns the file's source representation. For unmutated
// inputs the result is byte-exact with the original; mutated entries
// (via [File.Set]) are freshly rendered with default quoting while
// surrounding entries retain their original bytes.
func (f *File) Marshal() ([]byte, error) {
	if f == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	for _, e := range f.entries {
		buf.Write(e.raw)
	}
	return buf.Bytes(), nil
}

// renderAssignmentLine produces source bytes for a fresh assignment:
// bare value when safe, double-quoted otherwise. The line is
// terminated by ending (typically "\n").
func renderAssignmentLine(key, value, ending string) []byte {
	var b bytes.Buffer
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(formatValue(value))
	b.WriteString(ending)
	return b.Bytes()
}

func formatValue(value string) string {
	if !needsQuoting(value) {
		return value
	}
	return quoteValue(value)
}

// needsQuoting reports whether value must be double-quoted. Any
// character that would alter line tokenization (whitespace, `#`, `$`,
// quotes, backslash, backtick) or any control character forces
// quoting.
func needsQuoting(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		case '#', '$', '"', '\'', '\\', '`':
			return true
		}
		if r < 0x20 || r == 0x7F {
			return true
		}
	}
	return false
}

// quoteValue double-quotes value with the standard escape table.
// Output is parseable by this package and by joho/godotenv.
func quoteValue(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '$':
			b.WriteString(`\$`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
