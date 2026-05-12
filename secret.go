package env

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
)

// Secret is a generic wrapper that hides T's value in textual output
// and integrates with the standard observability surfaces:
//
//   - [fmt.Stringer], [fmt.GoStringer]: return the redaction marker, so
//     fmt.Println / %v / %#v emit the marker, never the value.
//   - [slog.LogValuer]: emits the marker, so slog attributes redact
//     automatically.
//   - [Marshaler]: returns the underlying T for the reverse [Encode]
//     path. The encoder's secret-tag check normally redacts to "***"
//     first; MarshalEnv runs only under [WithEncodeIncludeSecrets].
//   - [Unmarshaler]: decodes via the standard decoder pipeline; works
//     for any T the decoder accepts.
//
// The wrapper and the secret tag option are interchangeable for
// redaction purposes:
//
//	type Config struct {
//	    Token env.Secret[string] `env:"TOKEN,required"`
//	    Other string             `env:"OTHER,required,secret"`
//	}
type Secret[T any] struct {
	v T
}

// NewSecret returns a Secret[T] holding v.
func NewSecret[T any](v T) Secret[T] {
	return Secret[T]{v: v}
}

// Value returns the underlying T. Use sparingly: every call expands
// the risk surface for accidental logging.
func (s Secret[T]) Value() T {
	return s.v
}

// String implements [fmt.Stringer] and returns the redaction marker.
func (s Secret[T]) String() string {
	return secretMarker
}

// GoString implements [fmt.GoStringer] and returns the redaction
// marker, so %#v formatting also redacts.
func (s Secret[T]) GoString() string {
	return secretMarker
}

// LogValue implements [slog.LogValuer] and returns the redaction
// marker.
func (s Secret[T]) LogValue() slog.Value {
	return slog.StringValue(secretMarker)
}

// MarshalJSON implements [encoding/json.Marshaler] and returns the
// redaction marker as a JSON string.
func (s Secret[T]) MarshalJSON() ([]byte, error) {
	return []byte(`"` + secretMarker + `"`), nil
}

// MarshalEnv implements [Marshaler] for the reverse [Encode] path.
// Renders the inner T via fmt.Sprint — round-trip stable for
// primitives; other types may need a custom marshaler.
func (s Secret[T]) MarshalEnv() (string, error) {
	return fmt.Sprint(s.v), nil
}

// UnmarshalEnv implements [Unmarshaler] by delegating to the package's
// decoder against the inner T. If T relies on [UnmarshalerContext], it
// receives context.Background() (the wrapper has no context to thread).
func (s *Secret[T]) UnmarshalEnv(text string) error {
	return decodeValue(context.Background(), reflect.ValueOf(&s.v).Elem(), text, decodeOpts{})
}

// secretMarker is intentionally distinct from the encoder's "***" so
// wrapper-redacted values stay distinguishable in logs.
const secretMarker = "<redacted>"
