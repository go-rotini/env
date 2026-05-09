package env

import "context"

// Unmarshaler is implemented by types that parse themselves from a
// string. The decoder calls UnmarshalEnv on the field's address, so
// types should declare the method with a pointer receiver.
type Unmarshaler interface {
	UnmarshalEnv(s string) error
}

// UnmarshalerContext is like [Unmarshaler] but receives a context.
// Invoked when loading through [Loader.LoadContext]; the loader
// prefers it over [Unmarshaler] when both are implemented.
type UnmarshalerContext interface {
	UnmarshalEnv(ctx context.Context, s string) error
}

// Marshaler is implemented by types that render themselves as a string
// for the reverse [Encode] path.
type Marshaler interface {
	MarshalEnv() (string, error)
}
