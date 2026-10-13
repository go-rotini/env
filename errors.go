package env

import (
	"errors"
	"fmt"
	"strings"
)

// MissingRequiredError is reported for a field marked required or
// notEmpty (or any field under [WithRequireAll]) whose key is absent
// from every Source and which has no default.
type MissingRequiredError struct {
	Field string
	Key   string
}

// Error names the missing variable and its field.
func (e *MissingRequiredError) Error() string {
	if e == nil || e.Field == "" {
		return "env: missing required variable"
	}
	return fmt.Sprintf("env: missing required variable %q (field %s)", e.Key, e.Field)
}

// Is matches any *MissingRequiredError, such as [ErrMissingRequired].
func (*MissingRequiredError) Is(target error) bool {
	_, ok := target.(*MissingRequiredError)
	return ok
}

// EmptyValueError is reported for a field marked notEmpty whose key
// is set to the empty string.
type EmptyValueError struct {
	Field string
	Key   string
}

// Error names the empty variable and its field.
func (e *EmptyValueError) Error() string {
	if e == nil || e.Field == "" {
		return "env: empty value for field marked notEmpty"
	}
	return fmt.Sprintf("env: empty value for %q (field %s, marked notEmpty)", e.Key, e.Field)
}

// Is matches any *EmptyValueError, such as [ErrEmptyValue].
func (*EmptyValueError) Is(target error) bool {
	_, ok := target.(*EmptyValueError)
	return ok
}

// ParseError is reported when a string value cannot be converted to a
// field's target Go type. Field, Key, Value, and Type may be empty when
// the error originates inside the decoder before the loader has wrapped
// it with field context. For a secret-tagged field the loader runs
// Value through the secret redactor and drops Cause.
type ParseError struct {
	Field string
	Key   string
	Value string
	Type  string
	Cause error
}

// Error describes the value, target type and cause, plus the field and
// key when known.
func (e *ParseError) Error() string {
	if e == nil {
		return "env: parse error"
	}
	var b strings.Builder
	b.WriteString("env: ")
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

// Is matches any *ParseError, such as [ErrParse].
func (*ParseError) Is(target error) bool {
	_, ok := target.(*ParseError)
	return ok
}

// Unwrap returns Cause.
func (e *ParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ValidationError wraps an error returned from a target struct's
// [Validator.Validate] (or [ValidatorContext.Validate]) method.
type ValidationError struct {
	Cause error
}

// Error prefixes the cause's text.
func (e *ValidationError) Error() string {
	if e == nil || e.Cause == nil {
		return "env: validation error"
	}
	return fmt.Sprintf("env: validation: %v", e.Cause)
}

// Is matches any *ValidationError, such as [ErrValidation].
func (*ValidationError) Is(target error) bool {
	_, ok := target.(*ValidationError)
	return ok
}

// Unwrap returns Cause.
func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ImmutableChangedError is reported when a reload candidate would have
// changed a field tagged immutable. Old and New are the strings the
// field was decoded from; they are run through the loader's secret
// redactor for secret, fromFile and expand fields, and for every field
// when the loader has a [Mutator] (whose output may be a resolved
// secret).
type ImmutableChangedError struct {
	Field string
	Key   string
	Old   string
	New   string
}

// Error names the field and key; it does not include Old or New.
func (e *ImmutableChangedError) Error() string {
	if e == nil {
		return "env: immutable field changed"
	}
	return fmt.Sprintf("env: immutable field %s (key %s) changed", e.Field, e.Key)
}

// Is matches any *ImmutableChangedError, such as [ErrImmutableChanged].
func (*ImmutableChangedError) Is(target error) bool {
	_, ok := target.(*ImmutableChangedError)
	return ok
}

// CycleError is reported when variable expansion (the `expand` tag
// option) does not terminate — a reference cycle (A → B → A) or
// excessive nesting in `${VAR:-default}` chains.
type CycleError struct {
	// Chain is the reference chain leading to the cycle, e.g.
	// ["DSN", "HOST", "DSN"]. It may hold a single element when the
	// non-termination is a self-referential default rather than a
	// genuine cross-variable cycle.
	Chain []string
}

// Error renders the reference chain.
func (e *CycleError) Error() string {
	if e == nil || len(e.Chain) == 0 {
		return "env: variable expansion cycle"
	}
	return fmt.Sprintf("env: variable expansion cycle: %s", strings.Join(e.Chain, " -> "))
}

// Is matches any *CycleError, such as [ErrCycle].
func (*CycleError) Is(target error) bool {
	_, ok := target.(*CycleError)
	return ok
}

// MultiError aggregates the per-field errors, and any validation
// error, from a single Load call. Its Unwrap() []error method lets
// [errors.Is] and [errors.As] walk every branch.
type MultiError struct {
	Errors []error
}

// Error returns the single error's text, or a count followed by one
// line per error.
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

// Unwrap returns the aggregated errors for [errors.Is] / [errors.As]
// walking.
func (m *MultiError) Unwrap() []error {
	if m == nil {
		return nil
	}
	return m.Errors
}

// Is matches any *MultiError, such as [ErrMultiError].
func (*MultiError) Is(target error) bool {
	_, ok := target.(*MultiError)
	return ok
}

// Append appends err to the aggregator. Nil errors are ignored.
func (m *MultiError) Append(err error) {
	if err == nil {
		return
	}
	m.Errors = append(m.Errors, err)
}

// Sentinel errors. The struct-pointer sentinels match by type via each
// error's Is method; the [errors.New] sentinels match by value identity.
// Never pass a struct-pointer sentinel's address to [errors.As] (as in
// &ErrParse): that overwrites the shared sentinel. Declare a variable of
// the error type for errors.As instead.
var (
	// ErrMissingRequired matches any [*MissingRequiredError].
	ErrMissingRequired = &MissingRequiredError{}

	// ErrEmptyValue matches any [*EmptyValueError].
	ErrEmptyValue = &EmptyValueError{}

	// ErrParse matches any [*ParseError].
	ErrParse = &ParseError{}

	// ErrValidation matches any [*ValidationError].
	ErrValidation = &ValidationError{}

	// ErrImmutableChanged matches any [*ImmutableChangedError].
	ErrImmutableChanged = &ImmutableChangedError{}

	// ErrCycle matches any [*CycleError].
	ErrCycle = &CycleError{}

	// ErrMultiError matches any [*MultiError].
	ErrMultiError = &MultiError{}

	// ErrNilPointer is returned when [Loader.Load] is called with a nil
	// pointer or a non-pointer.
	ErrNilPointer = errors.New("env: non-nil pointer to struct required")

	// ErrNotStruct is returned when the target type is not a struct.
	ErrNotStruct = errors.New("env: target must be a struct or pointer to struct")

	// ErrUnknownTagOption is returned when a struct tag contains an
	// unrecognized option.
	ErrUnknownTagOption = errors.New("env: unknown tag option")

	// ErrConflictingFields is returned when two fields in the same
	// struct resolve to the same env-var key after prefix accumulation.
	ErrConflictingFields = errors.New("env: conflicting field names")

	// ErrRecursiveType is returned when a struct type contains itself
	// (directly or through pointers or embedding) along a path the
	// loader would walk. Tag the self-referencing field with env:"-" to
	// skip it.
	ErrRecursiveType = errors.New("env: recursive struct type")

	// ErrSeparatorInValue is returned by [Encode] when a slice, array or
	// map element contains the separator it would be joined with, or a
	// slice's only element is empty, so the output could not load back
	// as the same value.
	ErrSeparatorInValue = errors.New("env: element contains its separator")

	// ErrUnsupportedType is returned when a struct field's target type
	// cannot be decoded from a string by any built-in or interface
	// mechanism (chan, func, complex, bare any, struct without any
	// unmarshaler).
	ErrUnsupportedType = errors.New("env: unsupported target type")

	// ErrExpansionAssertion is returned (wrapped, with field context)
	// when a `${VAR:?message}` reference in an `expand`-tagged value
	// names a variable that is not set.
	ErrExpansionAssertion = errors.New("env: ${VAR:?...} assertion failed")

	// ErrFromFileRead is returned when a fromFile-tagged field
	// references a path that cannot be read.
	ErrFromFileRead = errors.New("env: fromFile target is not readable")
)

// FormatError renders err into a human-readable string. For
// [*MultiError], each wrapped error is rendered on its own line.
// Returns "" when err is nil. See [FormatErrorColor] for an
// ANSI-colored variant.
func FormatError(err error) string {
	return formatError(err, false)
}

// FormatErrorColor is like [FormatError] but wraps each message in
// ANSI bold-red. Use it only when the destination is a terminal.
func FormatErrorColor(err error) string {
	return formatError(err, true)
}

func formatError(err error, useColor bool) string {
	if err == nil {
		return ""
	}
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
