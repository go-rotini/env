package env

import (
	"errors"
	"fmt"
	"strings"
)

// Position identifies a location within source text. Zero-valued positions
// (Line == 0) mean "no position info available"; most env-var errors do
// not carry positions, only `.env` file errors do.
type Position struct {
	File   string
	Line   int
	Column int
	Offset int
}

func (p Position) String() string {
	if p.Line <= 0 {
		return ""
	}
	if p.File != "" {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
	}
	return fmt.Sprintf("line %d, column %d", p.Line, p.Column)
}

// MissingRequiredError is reported for a field with required (or notEmpty)
// whose key is absent from every Source and which has no default=.
type MissingRequiredError struct {
	Field string
	Key   string
	Pos   Position
}

func (e *MissingRequiredError) Error() string {
	if e == nil {
		return "env: missing required variable"
	}
	if e.Field == "" {
		return "env: missing required variable"
	}
	return fmt.Sprintf("env: missing required variable %q (field %s)", e.Key, e.Field)
}

func (*MissingRequiredError) Is(target error) bool {
	_, ok := target.(*MissingRequiredError)
	return ok
}

// EmptyValueError is reported for a field with notEmpty whose key is set
// to the empty string.
type EmptyValueError struct {
	Field string
	Key   string
	Pos   Position
}

func (e *EmptyValueError) Error() string {
	if e == nil {
		return "env: empty value for field marked notEmpty"
	}
	if e.Field == "" {
		return "env: empty value for field marked notEmpty"
	}
	return fmt.Sprintf("env: empty value for %q (field %s, marked notEmpty)", e.Key, e.Field)
}

func (*EmptyValueError) Is(target error) bool {
	_, ok := target.(*EmptyValueError)
	return ok
}

// ParseError is reported when a string value cannot be converted to a
// field's target Go type (numeric overflow, invalid duration, etc.).
//
// Field, Key, and Pos may be empty when the error originates inside the
// decoder before the loader has wrapped it with field context; the loader
// fills these in before adding the error to its [MultiError].
type ParseError struct {
	Field string
	Key   string
	Value string // raw value (replaced with a redaction marker for secret-tagged fields in Phase 7+)
	Type  string
	Cause error
	Pos   Position
}

func (e *ParseError) Error() string {
	if e == nil {
		return "env: parse error"
	}
	var b strings.Builder
	b.WriteString("env: ")
	if e.Pos.Line > 0 {
		fmt.Fprintf(&b, "%s: ", e.Pos.String())
	}
	switch {
	case e.Type == "" && e.Cause == nil:
		b.WriteString("parse error")
	case e.Type == "":
		fmt.Fprintf(&b, "parse error: %v", e.Cause)
	default:
		fmt.Fprintf(&b, "cannot parse %q as %s", e.Value, e.Type)
		if e.Cause != nil {
			fmt.Fprintf(&b, ": %v", e.Cause)
		}
	}
	if e.Field != "" {
		fmt.Fprintf(&b, " (field %s", e.Field)
		if e.Key != "" {
			fmt.Fprintf(&b, ", key %s", e.Key)
		}
		b.WriteByte(')')
	}
	return b.String()
}

func (*ParseError) Is(target error) bool {
	_, ok := target.(*ParseError)
	return ok
}

func (e *ParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SyntaxError is reported when a `.env` file is malformed (unterminated
// quote, bad escape, etc.). Phase 9's dotenv parser produces these; the
// type is defined here so the rest of the package can reference it.
type SyntaxError struct {
	Pos     Position
	Message string
}

func (e *SyntaxError) Error() string {
	if e == nil {
		return "env: syntax error"
	}
	if e.Pos.Line > 0 {
		return fmt.Sprintf("env: %s: %s", e.Pos.String(), e.Message)
	}
	return fmt.Sprintf("env: %s", e.Message)
}

func (*SyntaxError) Is(target error) bool {
	_, ok := target.(*SyntaxError)
	return ok
}

// ValidationError wraps an error returned from a target struct's
// Validate() method. Phase 7 wires the validator hook; the type is
// defined here for forward compatibility.
type ValidationError struct {
	Cause error
}

func (e *ValidationError) Error() string {
	if e == nil || e.Cause == nil {
		return "env: validation error"
	}
	return fmt.Sprintf("env: validation: %v", e.Cause)
}

func (*ValidationError) Is(target error) bool {
	_, ok := target.(*ValidationError)
	return ok
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ImmutableChangedError is reported when a Live[T] reload candidate
// would have changed a field tagged with immutable. Phase 11 produces
// these; the type is defined here for forward compatibility.
type ImmutableChangedError struct {
	Field string
	Key   string
	Old   string // redacted for secret-tagged fields
	New   string // redacted for secret-tagged fields
}

func (e *ImmutableChangedError) Error() string {
	if e == nil {
		return "env: immutable field changed"
	}
	return fmt.Sprintf("env: immutable field %s (key %s) changed", e.Field, e.Key)
}

func (*ImmutableChangedError) Is(target error) bool {
	_, ok := target.(*ImmutableChangedError)
	return ok
}

// CycleError is reported when variable expansion encounters a reference
// cycle (A → B → A). Phase 9's dotenv parser produces these.
type CycleError struct {
	Chain []string
	Pos   Position
}

func (e *CycleError) Error() string {
	if e == nil || len(e.Chain) == 0 {
		return "env: variable expansion cycle"
	}
	return fmt.Sprintf("env: variable expansion cycle: %s", strings.Join(e.Chain, " -> "))
}

func (*CycleError) Is(target error) bool {
	_, ok := target.(*CycleError)
	return ok
}

// MultiError aggregates one or more errors from a single Load call. It
// implements the Go 1.20+ Unwrap() []error convention so [errors.Is] and
// [errors.As] walk every branch.
type MultiError struct {
	Errors []error
}

func (m *MultiError) Error() string {
	if m == nil || len(m.Errors) == 0 {
		return "env: no errors"
	}
	if len(m.Errors) == 1 {
		return m.Errors[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "env: %d errors:", len(m.Errors))
	for _, e := range m.Errors {
		fmt.Fprintf(&b, "\n  - %s", e.Error())
	}
	return b.String()
}

// Unwrap returns the wrapped error list. The Go 1.20+ multi-error form is
// honored by errors.Is and errors.As, so callers can test whether ANY of
// the aggregated errors matches a sentinel.
func (m *MultiError) Unwrap() []error {
	if m == nil {
		return nil
	}
	return m.Errors
}

func (*MultiError) Is(target error) bool {
	_, ok := target.(*MultiError)
	return ok
}

// Append appends err to the aggregator, ignoring nil.
func (m *MultiError) Append(err error) {
	if err == nil {
		return
	}
	m.Errors = append(m.Errors, err)
}

// Sentinel errors. The struct-pointer sentinels (those constructed as
// &XError{}) match by type via the corresponding Is method; the
// errors.New sentinels match by value identity.
var (
	// ErrMissingRequired matches any [*MissingRequiredError].
	ErrMissingRequired = &MissingRequiredError{}

	// ErrEmptyValue matches any [*EmptyValueError].
	ErrEmptyValue = &EmptyValueError{}

	// ErrParse matches any [*ParseError]. Replaces the basic sentinel
	// from Phase 4 with the full structured form.
	ErrParse = &ParseError{}

	// ErrSyntax matches any [*SyntaxError].
	ErrSyntax = &SyntaxError{}

	// ErrValidation matches any [*ValidationError].
	ErrValidation = &ValidationError{}

	// ErrImmutableChanged matches any [*ImmutableChangedError].
	ErrImmutableChanged = &ImmutableChangedError{}

	// ErrCycle matches any [*CycleError].
	ErrCycle = &CycleError{}

	// ErrMultiError matches any [*MultiError].
	ErrMultiError = &MultiError{}

	// ErrNilPointer is returned when [Loader.Load] is called with a nil
	// pointer, a non-pointer, or a pointer to a non-struct value.
	ErrNilPointer = errors.New("env: non-nil pointer to struct required")

	// ErrNotStruct is returned when the target type is not a struct.
	ErrNotStruct = errors.New("env: target must be a struct or pointer to struct")

	// ErrUnknownTagOption is returned when a struct tag contains an
	// option the loader does not recognize.
	ErrUnknownTagOption = errors.New("env: unknown tag option")

	// ErrConflictingFields is returned when two fields in the same
	// struct (after prefix accumulation) resolve to the same env-var key.
	ErrConflictingFields = errors.New("env: conflicting field names")

	// ErrUnsupportedType is returned when a struct field's target type
	// cannot be decoded from a string by any built-in or interface
	// mechanism (chan, func, complex, bare interface{}, struct without
	// any unmarshaler interface).
	ErrUnsupportedType = errors.New("env: unsupported target type")

	// ErrUnsetExpansion is returned (under WithStrictExpansion) when
	// variable expansion encounters an unset variable. Phase 9 wires
	// this; the sentinel exists here for forward compatibility.
	ErrUnsetExpansion = errors.New("env: variable referenced in expansion is unset")

	// ErrExpansionAssertion is returned when a ${VAR:?msg} assertion
	// fails during expansion. Phase 9.
	ErrExpansionAssertion = errors.New("env: ${VAR:?...} assertion failed")

	// ErrFromFileRead is returned when a fromFile-tagged field references
	// a path that cannot be read.
	ErrFromFileRead = errors.New("env: fromFile target is not readable")

	// ErrBlockSizeExceeded is returned when a Snapshot's rendered total
	// size exceeds a platform block-size limit.
	ErrBlockSizeExceeded = errors.New("env: rendered env block exceeds platform limit")
)

// FormatError renders err into a human-readable string. For [*MultiError],
// every wrapped error is rendered on its own line. Pass color=true to
// include ANSI bold-red escape sequences around each error message.
//
// FormatError returns the empty string when err is nil.
func FormatError(err error, color ...bool) string {
	if err == nil {
		return ""
	}
	useColor := len(color) > 0 && color[0]
	var multi *MultiError
	if errors.As(err, &multi) && multi != nil && len(multi.Errors) > 0 {
		var b strings.Builder
		for i, sub := range multi.Errors {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(formatSingleErr(sub, useColor))
		}
		return b.String()
	}
	return formatSingleErr(err, useColor)
}

func formatSingleErr(err error, useColor bool) string {
	s := err.Error()
	if useColor {
		return "\x1b[1;31m" + s + "\x1b[0m"
	}
	return s
}
