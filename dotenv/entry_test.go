package dotenv

import (
	"slices"
	"testing"
)

func TestFile_GetSetDelete(t *testing.T) {
	f := mustParse(t, "FOO=1\nBAR=2\n")

	if v, ok := f.Get("FOO"); !ok || v != "1" {
		t.Errorf("Get(FOO) = (%q, %v)", v, ok)
	}

	// Set existing key.
	f.Set("FOO", "updated")
	if v, _ := f.Get("FOO"); v != "updated" {
		t.Errorf("after Set: %q", v)
	}

	// Set new key (appended at end).
	f.Set("NEW", "added")
	if v, _ := f.Get("NEW"); v != "added" {
		t.Errorf("after new Set: %q", v)
	}

	// Delete existing.
	if !f.Delete("BAR") {
		t.Error("Delete returned false for existing key")
	}
	if _, ok := f.Get("BAR"); ok {
		t.Error("BAR still present after Delete")
	}

	// Delete non-existing.
	if f.Delete("NOPE") {
		t.Error("Delete returned true for non-existing key")
	}
}

func TestFile_LastWriteWins(t *testing.T) {
	f := mustParse(t, "FOO=first\nFOO=second\n")
	if v, _ := f.Get("FOO"); v != "second" {
		t.Errorf("last-write-wins: got %q", v)
	}
}

func TestFile_Keys(t *testing.T) {
	f := mustParse(t, "A=1\nB=2\nC=3\nA=4\n")
	got := f.Keys()
	want := []string{"A", "B", "C"} // first-occurrence order; no duplicates
	if !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
}

func TestFile_ToMap(t *testing.T) {
	f := mustParse(t, "A=1\nB=2\nA=overridden\n")
	got := f.ToMap()
	want := map[string]string{"A": "overridden", "B": "2"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ToMap()[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestFile_NilSafety(t *testing.T) {
	var f *File
	if v, ok := f.Get("FOO"); ok || v != "" {
		t.Errorf("nil File Get = (%q, %v)", v, ok)
	}
	if f.Delete("FOO") {
		t.Error("nil File Delete returned true")
	}
	if k := f.Keys(); k != nil {
		t.Errorf("nil File Keys = %v", k)
	}
	if m := f.ToMap(); m != nil {
		t.Errorf("nil File ToMap = %v", m)
	}
	if e := f.Entries(); e != nil {
		t.Errorf("nil File Entries = %v", e)
	}
	if p := f.Path(); p != "" {
		t.Errorf("nil File Path = %q", p)
	}
	f.Set("FOO", "x") // should not panic
}

func TestFile_EntriesAndPath(t *testing.T) {
	f := mustParse(t, "FOO=1\n# comment\n")
	if len(f.Entries()) != 2 {
		t.Errorf("got %d entries", len(f.Entries()))
	}
	// Path is empty for in-memory parses.
	if f.Path() != "" {
		t.Errorf("Path = %q", f.Path())
	}
}
