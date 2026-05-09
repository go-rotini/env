package env

import "context"

// Unmarshaler is implemented by types that parse themselves from a string.
// The single-string form covers most cases; for context-aware decoding
// (for example, reading from a remote secret store) use [UnmarshalerContext].
//
// The decoder calls UnmarshalEnv on the field's address, so types should
// declare the method with a pointer receiver.
type Unmarshaler interface {
	UnmarshalEnv(s string) error
}

// UnmarshalerContext is like [Unmarshaler] but receives a context.
// It is invoked when the load is performed through [Loader.LoadContext];
// in non-context loads, [Unmarshaler] is consulted instead. Types should
// implement only one of the two interfaces (the loader prefers
// UnmarshalerContext when both are available).
type UnmarshalerContext interface {
	UnmarshalEnv(ctx context.Context, s string) error
}

// Marshaler is implemented by types that render themselves as a string for
// the reverse [Encode] path.
type Marshaler interface {
	MarshalEnv() (string, error)
}
