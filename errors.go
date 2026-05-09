package env

import "errors"

// Sentinel errors returned by the loader.
//
// More error types and their concrete forms are introduced in later phases;
// this file currently contains only the sentinels needed by tag parsing
// and field collection.
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
)
