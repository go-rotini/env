package env

import (
	"errors"
	"strings"
	"testing"
)

func TestNilReceiverError(t *testing.T) {
	// Each error type's Error() and Unwrap() must handle a nil receiver
	// gracefully (returning a sensible string and nil respectively). This
	// is defensive against, e.g., programmers passing typed nils through
	// errors.As.
	cases := []struct {
		name string
		fn   func() string
	}{
		{"MissingRequiredError", func() string { return (*MissingRequiredError)(nil).Error() }},
		{"EmptyValueError", func() string { return (*EmptyValueError)(nil).Error() }},
		{"ParseError", func() string { return (*ParseError)(nil).Error() }},
		{"SyntaxError", func() string { return (*SyntaxError)(nil).Error() }},
		{"ValidationError", func() string { return (*ValidationError)(nil).Error() }},
		{"ImmutableChangedError", func() string { return (*ImmutableChangedError)(nil).Error() }},
		{"CycleError", func() string { return (*CycleError)(nil).Error() }},
		{"MultiError", func() string { return (*MultiError)(nil).Error() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fn() == "" {
				t.Errorf("nil-receiver Error() returned empty string")
			}
		})
	}

	// Unwrap on nil receivers.
	if u := (*ParseError)(nil).Unwrap(); u != nil {
		t.Errorf("nil ParseError Unwrap = %v, want nil", u)
	}
	if u := (*ValidationError)(nil).Unwrap(); u != nil {
		t.Errorf("nil ValidationError Unwrap = %v, want nil", u)
	}
}

func TestPosition_String(t *testing.T) {
	cases := []struct {
		p    Position
		want string
	}{
		{Position{}, ""},
		{Position{Line: 5, Column: 7}, "line 5, column 7"},
		{Position{File: "x.env", Line: 5, Column: 7}, "x.env:5:7"},
	}
	for _, tc := range cases {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("Position(%+v).String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestMissingRequiredError(t *testing.T) {
	e := &MissingRequiredError{Field: "DB.URL", Key: "APP_DB_URL"}
	msg := e.Error()
	if !strings.Contains(msg, "APP_DB_URL") || !strings.Contains(msg, "DB.URL") {
		t.Errorf("Error() = %q; expected key + field present", msg)
	}
	if !errors.Is(e, ErrMissingRequired) {
		t.Errorf("errors.Is fails for MissingRequiredError")
	}
	// Empty form
	if (&MissingRequiredError{}).Error() == "" {
		t.Error("empty MissingRequiredError produced empty Error()")
	}
}

func TestEmptyValueError(t *testing.T) {
	e := &EmptyValueError{Field: "Token", Key: "TOKEN"}
	if !strings.Contains(e.Error(), "TOKEN") {
		t.Errorf("Error() does not include key: %q", e.Error())
	}
	if !errors.Is(e, ErrEmptyValue) {
		t.Errorf("errors.Is fails for EmptyValueError")
	}
	if (&EmptyValueError{}).Error() == "" {
		t.Error("empty EmptyValueError produced empty Error()")
	}
}

func TestParseError(t *testing.T) {
	e := &ParseError{
		Field: "Port",
		Key:   "APP_PORT",
		Value: "abc",
		Type:  "int",
		Cause: errors.New("invalid syntax"),
	}
	msg := e.Error()
	for _, want := range []string{"abc", "int", "Port", "APP_PORT", "invalid syntax"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() missing %q: %q", want, msg)
		}
	}
	if !errors.Is(e, ErrParse) {
		t.Errorf("errors.Is fails for ParseError")
	}
	// Unwrap chain
	if !errors.Is(e, e.Cause) {
		t.Errorf("Unwrap chain broken")
	}
}

func TestParseError_EmptyForms(t *testing.T) {
	if (*ParseError)(nil).Error() == "" {
		t.Error("nil ParseError produced empty Error()")
	}
	if (&ParseError{}).Error() == "" {
		t.Error("empty ParseError produced empty Error()")
	}
	// With Pos but no other fields
	pos := Position{File: "x.env", Line: 3, Column: 5}
	if got := (&ParseError{Pos: pos, Cause: errors.New("oops")}).Error(); !strings.Contains(got, "x.env:3:5") {
		t.Errorf("Pos not rendered: %q", got)
	}
	// With cause but no type
	if got := (&ParseError{Cause: errors.New("oops")}).Error(); !strings.Contains(got, "oops") {
		t.Errorf("cause not rendered without Type: %q", got)
	}
}

func TestParseError_Unwrap(t *testing.T) {
	cause := errors.New("inner")
	e := &ParseError{Cause: cause}
	if u := errors.Unwrap(e); u != cause {
		t.Errorf("Unwrap = %v, want %v", u, cause)
	}
	if u := errors.Unwrap(&ParseError{}); u != nil {
		t.Errorf("Unwrap on no-cause = %v, want nil", u)
	}
}

func TestSyntaxError(t *testing.T) {
	e := &SyntaxError{Pos: Position{Line: 5, Column: 3}, Message: "bad escape"}
	if !strings.Contains(e.Error(), "bad escape") {
		t.Errorf("Error() missing message: %q", e.Error())
	}
	if !errors.Is(e, ErrSyntax) {
		t.Errorf("errors.Is fails for SyntaxError")
	}
	// No-position form
	if !strings.Contains((&SyntaxError{Message: "x"}).Error(), "x") {
		t.Error("missing message")
	}
	if (&SyntaxError{}).Error() == "" {
		t.Error("nil SyntaxError produced empty Error()")
	}
}

func TestValidationError(t *testing.T) {
	cause := errors.New("constraint failed")
	e := &ValidationError{Cause: cause}
	if !strings.Contains(e.Error(), "constraint failed") {
		t.Errorf("Error() missing cause: %q", e.Error())
	}
	if !errors.Is(e, ErrValidation) {
		t.Errorf("errors.Is fails for ValidationError")
	}
	if errors.Unwrap(e) != cause {
		t.Errorf("Unwrap broken")
	}
	if (&ValidationError{}).Error() == "" {
		t.Error("empty ValidationError produced empty Error()")
	}
	if errors.Unwrap(&ValidationError{}) != nil {
		t.Error("nil cause should unwrap to nil")
	}
}

func TestImmutableChangedError(t *testing.T) {
	e := &ImmutableChangedError{Field: "Port", Key: "PORT", Old: "8080", New: "9090"}
	msg := e.Error()
	if !strings.Contains(msg, "Port") || !strings.Contains(msg, "PORT") {
		t.Errorf("Error() missing field/key: %q", msg)
	}
	if !errors.Is(e, ErrImmutableChanged) {
		t.Errorf("errors.Is fails for ImmutableChangedError")
	}
	if (&ImmutableChangedError{}).Error() == "" {
		t.Error("empty form produced empty Error()")
	}
}

func TestCycleError(t *testing.T) {
	e := &CycleError{Chain: []string{"A", "B", "C", "A"}}
	if !strings.Contains(e.Error(), "A -> B -> C -> A") {
		t.Errorf("Error() does not render chain: %q", e.Error())
	}
	if !errors.Is(e, ErrCycle) {
		t.Errorf("errors.Is fails for CycleError")
	}
	if (&CycleError{}).Error() == "" {
		t.Error("empty form produced empty Error()")
	}
}

func TestMultiError_Single(t *testing.T) {
	inner := errors.New("solo")
	m := &MultiError{Errors: []error{inner}}
	if m.Error() != "solo" {
		t.Errorf("single-error MultiError should render the inner message: got %q", m.Error())
	}
	if !errors.Is(m, inner) {
		t.Errorf("errors.Is should find inner via Unwrap()")
	}
}

func TestMultiError_Multi(t *testing.T) {
	m := &MultiError{Errors: []error{
		errors.New("one"),
		errors.New("two"),
		errors.New("three"),
	}}
	got := m.Error()
	if !strings.Contains(got, "3 errors") {
		t.Errorf("Error() missing count: %q", got)
	}
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() missing %q", want)
		}
	}
}

func TestMultiError_UnwrapList(t *testing.T) {
	a := errors.New("a")
	b := &ParseError{Cause: errors.New("b")}
	m := &MultiError{Errors: []error{a, b}}

	if !errors.Is(m, a) {
		t.Errorf("errors.Is(m, a) failed; Unwrap() []error chain broken")
	}
	if !errors.Is(m, ErrParse) {
		t.Errorf("errors.Is(m, ErrParse) failed via *ParseError branch")
	}
	if !errors.Is(m, ErrMultiError) {
		t.Errorf("errors.Is(m, ErrMultiError) failed (sentinel)")
	}
}

func TestMultiError_Empty(t *testing.T) {
	if (&MultiError{}).Error() == "" {
		t.Error("empty MultiError produced empty Error()")
	}
	if (*MultiError)(nil).Error() == "" {
		t.Error("nil MultiError produced empty Error()")
	}
	if u := (*MultiError)(nil).Unwrap(); u != nil {
		t.Errorf("nil Unwrap = %v, want nil", u)
	}
}

func TestMultiError_Append(t *testing.T) {
	m := &MultiError{}
	m.Append(nil) // nil should be ignored
	m.Append(errors.New("a"))
	m.Append(errors.New("b"))
	if len(m.Errors) != 2 {
		t.Errorf("got %d errors, want 2", len(m.Errors))
	}
}

func TestFormatError_Nil(t *testing.T) {
	if got := FormatError(nil); got != "" {
		t.Errorf("FormatError(nil) = %q, want empty", got)
	}
}

func TestFormatError_Single(t *testing.T) {
	got := FormatError(errors.New("boom"))
	if !strings.Contains(got, "boom") {
		t.Errorf("got %q", got)
	}
}

func TestFormatError_Multi(t *testing.T) {
	m := &MultiError{Errors: []error{
		&ParseError{Field: "Port", Type: "int", Value: "abc", Cause: errors.New("bad")},
		&MissingRequiredError{Field: "URL", Key: "APP_URL"},
	}}
	got := FormatError(m)
	if !strings.Contains(got, "Port") || !strings.Contains(got, "URL") {
		t.Errorf("MultiError lines missing: %q", got)
	}
	if strings.Count(got, "\n") < 1 {
		t.Errorf("expected multi-line output, got %q", got)
	}
}

func TestFormatError_Color(t *testing.T) {
	got := FormatError(errors.New("boom"), true)
	if !strings.Contains(got, "\x1b[1;31m") || !strings.Contains(got, "\x1b[0m") {
		t.Errorf("color output missing ANSI sequences: %q", got)
	}
	got = FormatError(errors.New("boom"), false)
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-color output contains ANSI: %q", got)
	}
}
