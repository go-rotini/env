package dotenv

import (
	"slices"

	"github.com/go-rotini/env"
)

// EntryKind classifies one logical line in a `.env` file.
type EntryKind int

const (
	// EntryBlank is a whitespace-only line.
	EntryBlank EntryKind = iota

	// EntryComment is a line whose first non-whitespace character is
	// `#`. Trailing comments on assignment lines are stored on the
	// assignment, not as separate EntryComment entries.
	EntryComment

	// EntryAssignment is a `KEY=VALUE` (or `KEY: VALUE`) line.
	EntryAssignment
)

// Entry is one logical line in a parsed `.env` file. Most callers use
// [File.Get] / [File.Set] / [File.Delete] / [File.Keys] instead of
// reading the entry list directly.
type Entry struct {
	Kind EntryKind

	// Key is the assignment key (without the `export ` prefix), empty
	// for non-assignment entries.
	Key string

	// Value is the decoded value: escapes interpreted, quoting
	// stripped, and (with [WithExpand]) variable references resolved.
	// Empty for non-assignment entries.
	Value string

	// Pos is the source position of the entry's first byte.
	Pos env.Position

	// quote is the value's source quoting form (0, '"', '\'', or '`')
	// — internal to the parse → expand pipeline.
	quote byte

	// raw is the verbatim source text including the line ending.
	// Marshal emits this slice unchanged for unmutated entries.
	raw []byte
}

// File is the AST for a parsed `.env` source. Entry order and per-
// entry source bytes are preserved so [File.Marshal] is byte-exact on
// unmutated inputs.
type File struct {
	entries []Entry
	path    string
}

// Path returns the file path the AST was loaded from, or "" for an
// in-memory [Parse].
func (f *File) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// Entries returns the entry list. The slice is not for caller
// mutation; use [File.Set] / [File.Delete].
func (f *File) Entries() []Entry {
	if f == nil {
		return nil
	}
	return f.entries
}

// Get returns the value associated with key. When the key appears in
// multiple assignments the LAST occurrence wins (matching `source
// <file>` in Bash).
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

// Set assigns value to key. An existing assignment is updated in
// place; otherwise a new assignment is appended. The new value is
// rendered with sensible-default quoting; round-trip is byte-exact
// except for the mutated line.
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

// Delete removes every assignment for key. Returns true when at least
// one entry was removed. Comments and blank lines are unaffected.
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

// Keys returns assignment keys in insertion order, deduplicated to the
// first occurrence.
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

// ToMap returns the file's effective key/value mapping (last-write-
// wins per Bash convention).
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
