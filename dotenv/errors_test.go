package dotenv

import (
	"errors"
	"testing"

	"github.com/go-rotini/env"
)

func TestParseError(t *testing.T) {
	e := &ParseError{
		Pos:     env.Position{File: "x.env", Line: 5, Column: 3},
		Message: "bad token",
	}
	got := e.Error()
	for _, want := range []string{"x.env", "5", "3", "bad token"} {
		if !contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
	if !errors.Is(e, ErrParse) {
		t.Error("errors.Is fails for *ParseError")
	}

	// Without file path.
	e2 := &ParseError{Pos: env.Position{Line: 5, Column: 3}, Message: "x"}
	if !contains(e2.Error(), "line 5") {
		t.Errorf("got %q", e2.Error())
	}

	// Without position.
	e3 := &ParseError{Message: "x"}
	if e3.Error() == "" {
		t.Error("empty Error()")
	}

	// Nil receiver.
	if (*ParseError)(nil).Error() == "" {
		t.Error("nil Error()")
	}
}

func TestExpansionError(t *testing.T) {
	e := &ExpansionError{
		Pos:      env.Position{Line: 3},
		Variable: "MISSING",
		Reason:   "unset",
		Cause:    errors.New("inner"),
	}
	got := e.Error()
	for _, want := range []string{"line 3", "MISSING", "unset"} {
		if !contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
	if !errors.Is(e, ErrExpansion) {
		t.Error("errors.Is fails for *ExpansionError")
	}
	if errors.Unwrap(e) == nil {
		t.Error("Unwrap returned nil")
	}

	// Without position.
	e2 := &ExpansionError{Variable: "X", Reason: "y"}
	if !contains(e2.Error(), "X") {
		t.Errorf("got %q", e2.Error())
	}

	// Without variable or position.
	e3 := &ExpansionError{Reason: "general"}
	if !contains(e3.Error(), "general") {
		t.Errorf("got %q", e3.Error())
	}

	// Nil receiver.
	if (*ExpansionError)(nil).Error() == "" {
		t.Error("nil Error()")
	}
	if (*ExpansionError)(nil).Unwrap() != nil {
		t.Error("nil Unwrap")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexAny(s, sub) >= 0)
}

func indexAny(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
