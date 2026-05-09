package dotenv

import (
	"slices"

	"github.com/go-rotini/env"
)

// EntryKind classifies one logical line in a `.env` file.
type EntryKind int

const (
	// EntryBlank is a line containing only whitespace (or the empty line
	// at end of file).
	EntryBlank EntryKind = iota

	// EntryComment is a line whose first non-whitespace character is `#`.
	// Trailing comments on assignment lines are stored on the assignment
	// entry, not as separate EntryComment entries.
	EntryComment

	// EntryAssignment is a `KEY=VALUE` (or `KEY: VALUE`) line.
	EntryAssignment
)

// Entry is one logical line in a parsed [`.env`] file. Most callers
// interact with entries indirectly through [File.Get] / [File.Set] /
// [File.Delete] / [File.Keys]; the entry list is exposed only via
// [File.Marshal] (which uses each entry's preserved source bytes for
// byte-exact round-trip) and via direct AST traversal for advanced
// tooling.
type Entry struct {
	Kind EntryKind

	// Key is the assignment key (without the `export ` prefix). Empty
	// for non-assignment entries.
	Key string

	// Value is the decoded value: escape sequences interpreted, quoting
	// stripped, and (when [WithExpand] was set during parse) variable
	// references resolved. Empty for non-assignment entries.
	Value string

	// Pos is the source position of the entry's first byte.
	Pos env.Position

	// quote is the value's source quoting form: 0 (unquoted), '"', '\'',
	// or '`'. Used by the expansion pass to skip single-quoted and
	// backtick-quoted values. Unexported because it's an implementation
	// detail of the parse → expand pipeline.
	quote byte

	// raw is the verbatim source text of this entry, including the line
	// ending. Marshal emits this slice unchanged. Set methods update
	// this from the new key/value to keep the AST consistent on round-
	// trip; comments and blank lines retain their original raw bytes.
	raw []byte
}

// File is the AST for a parsed `.env` source. It preserves entry order
// and per-entry source bytes so [File.Marshal] is byte-exact on
// canonical (unmutated) inputs.
type File struct {
	entries []Entry
	path    string // set when the file was loaded from disk; "" for in-memory parses
}

// Path returns the file path the AST was loaded from, or empty when the
// AST originated from a [Parse] call against in-memory bytes.
func (f *File) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// Entries returns the entry list. Callers should not mutate the returned
// slice; use [File.Set] / [File.Delete] instead.
func (f *File) Entries() []Entry {
	if f == nil {
		return nil
	}
	return f.entries
}

// Get returns the value associated with key and whether the key is
// present. When the same key appears in multiple assignment entries,
// the LAST occurrence wins (matches the behavior of `source <file>`
// in Bash).
func (f *File) Get(key string) (value string, ok bool) {
	if f == nil {
		return "", false
	}
	for _, e := range slices.Backward(f.entries) {
		if e.Kind == EntryAssignment && e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

// Set assigns value to key. If an assignment for key already exists,
// its value is updated in place (preserving its position and any
// surrounding comments/blank lines); otherwise a new assignment entry
// is appended at the end of the file.
//
// The new value is rendered using sensible-default quoting rules
// ([needsQuoting] / [quoteValue] from the encoder family). A round-trip
// of unmutated keys remains byte-exact; a round-trip of a mutated key
// is byte-exact except for the mutated line itself.
func (f *File) Set(key, value string) {
	if f == nil {
		return
	}
	for i := range f.entries {
		if f.entries[i].Kind == EntryAssignment && f.entries[i].Key == key {
			f.entries[i].Value = value
			f.entries[i].raw = renderAssignmentLine(key, value, "\n")
			return
		}
	}
	f.entries = append(f.entries, Entry{
		Kind:  EntryAssignment,
		Key:   key,
		Value: value,
		raw:   renderAssignmentLine(key, value, "\n"),
	})
}

// Delete removes every assignment entry whose key matches. Returns true
// if any entry was removed. Comments and blank lines are unaffected.
func (f *File) Delete(key string) bool {
	if f == nil {
		return false
	}
	removed := false
	f.entries = slices.DeleteFunc(f.entries, func(e Entry) bool {
		if e.Kind == EntryAssignment && e.Key == key {
			removed = true
			return true
		}
		return false
	})
	return removed
}

// Keys returns the list of assignment keys in insertion order. When the
// same key appears in multiple assignment entries, only the FIRST
// occurrence is included (so the slice has no duplicates while
// preserving the order in which keys first appeared).
func (f *File) Keys() []string {
	if f == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(f.entries))
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		if e.Kind != EntryAssignment {
			continue
		}
		if _, dup := seen[e.Key]; dup {
			continue
		}
		seen[e.Key] = struct{}{}
		out = append(out, e.Key)
	}
	return out
}

// ToMap returns the file's effective key/value mapping (last-write-wins
// per Bash convention).
func (f *File) ToMap() map[string]string {
	if f == nil {
		return nil
	}
	out := make(map[string]string, len(f.entries))
	for _, e := range f.entries {
		if e.Kind == EntryAssignment {
			out[e.Key] = e.Value
		}
	}
	return out
}
