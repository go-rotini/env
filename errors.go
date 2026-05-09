package env

import "errors"

// Sentinel errors returned by the loader.
//
// More error types and their concrete forms are introduced in later phases;
// this file currently contains the sentinels needed by tag parsing, field
// collection, and value decoding.
var (
	// ErrNotStruct is returned when a Load target is not a struct or
	// pointer to struct.
	ErrNotStruct = errors.New("env: target must be a struct or pointer to struct")

	// ErrUnknownTagOption is returned when a struct tag contains an
	// option the loader does not recognize.
	ErrUnknownTagOption = errors.New("env: unknown tag option")

	// ErrConflictingFields is returned when two fields in the same struct
	// (after prefix accumulation) resolve to the same env-var key.
	ErrConflictingFields = errors.New("env: conflicting field names")

	// ErrParse wraps any failure to convert a string value into a Go
	// field's target type (numeric overflow, malformed duration, etc.).
	ErrParse = errors.New("env: parse error")

	// ErrUnsupportedType is returned when a struct field's target type
	// cannot be decoded from a string by any built-in or interface
	// mechanism (for example: chan, func, complex64, complex128, or a
	// bare interface{} field).
	ErrUnsupportedType = errors.New("env: unsupported target type")
)
