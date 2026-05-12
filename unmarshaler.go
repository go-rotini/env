package env

import "context"

// Unmarshaler is implemented by types that parse themselves from a
// string. The decoder calls UnmarshalEnv on the field's address, so
// types should declare the method with a pointer receiver.
type Unmarshaler interface {
	UnmarshalEnv(s string) error
}

// UnmarshalerContext is like [Unmarshaler] but receives the context
// passed to [Loader.LoadContext] (or context.Background() for
// [Loader.Load] / the package-level [Load]). The loader checks a
// field's address for this interface and, failing that, for
// [Unmarshaler] — a type satisfies one or the other, never both (Go
// method names cannot be overloaded). It is consulted only at the top
// level of a field, not for slice elements or map values, which the
// (context-free) value decoder handles.
type UnmarshalerContext interface {
	UnmarshalEnv(ctx context.Context, s string) error
}

// Marshaler is implemented by types that render themselves as a string
// for the reverse [Encode] path.
type Marshaler interface {
	MarshalEnv() (string, error)
}
