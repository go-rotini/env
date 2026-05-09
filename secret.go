package env

import (
	"fmt"
	"log/slog"
	"reflect"
)

// Secret is a generic wrapper that hides T's value in textual output and
// integrates with the standard observability surfaces:
//
//   - [fmt.Stringer]: returns the redactor's marker string ("<redacted>"
//     by default). [fmt.Println(s)] emits the marker, never the value.
//   - [fmt.GoStringer]: returns the same marker; %#v formatting is safe.
//   - [slog.LogValuer]: returns a [slog.Value] holding the marker, so
//     [slog.Logger]s with the wrapper as an attribute redact automatically.
//   - [Marshaler]: [MarshalEnv] returns the inner T as a string for the
//     reverse Encode path. The encoder's secret-tag check redacts to "***"
//     before this method is invoked, so MarshalEnv runs only when the
//     loader's caller has explicitly opted into emitting raw secret
//     values via [WithEncodeIncludeSecrets].
//   - [Unmarshaler]: [UnmarshalEnv] decodes the source string into T using
//     the package's standard decoder pipeline. Works for any T the
//     decoder accepts (including custom T with their own Unmarshaler).
//
// Use either the wrapper directly:
//
//	type Config struct {
//	    Token env.Secret[string] `env:"TOKEN,required"`
//	}
//
// or the secret tag option (which redacts in error messages and
// Describe output without changing the field's type):
//
//	type Config struct {
//	    Token string `env:"TOKEN,required,secret"`
//	}
type Secret[T any] struct {
	v T
}

// NewSecret constructs a Secret[T] holding v.
func NewSecret[T any](v T) Secret[T] {
	return Secret[T]{v: v}
}

// Value returns the underlying T. Use sparingly — every call expands the
// risk surface for accidental logging or printing.
func (s Secret[T]) Value() T {
	return s.v
}

// String implements [fmt.Stringer], returning the redaction marker.
// fmt.Print, fmt.Println, %v / %s formatting all redact.
func (s Secret[T]) String() string {
	return secretMarker
}

// GoString implements [fmt.GoStringer], returning the redaction marker
// so %#v formatting redacts too.
func (s Secret[T]) GoString() string {
	return secretMarker
}

// LogValue implements [slog.LogValuer]. slog.Logger with the wrapper as
// an attribute (e.g., slog.Any("token", token)) redacts automatically.
func (s Secret[T]) LogValue() slog.Value {
	return slog.StringValue(secretMarker)
}

// MarshalJSON implements [encoding/json.Marshaler] so that JSON-rendered
// configs redact secret values too. Returns "<redacted>".
func (s Secret[T]) MarshalJSON() ([]byte, error) {
	return []byte(`"` + secretMarker + `"`), nil
}

// MarshalEnv implements [Marshaler] for the reverse Encode path. It
// renders the inner T using fmt.Sprint, which produces a faithful
// representation for primitives. Round-trip stability through the
// loader is guaranteed when T is a primitive ([string], all numeric
// kinds, bool); other types may need a custom marshaler if exact
// formatting matters.
//
// The encoder's secret-tag check redacts to "***" BEFORE this method
// is invoked in the normal flow; MarshalEnv runs only when the caller
// has explicitly opted in via [WithEncodeIncludeSecrets].
func (s Secret[T]) MarshalEnv() (string, error) {
	return fmt.Sprint(s.v), nil
}

// UnmarshalEnv implements [Unmarshaler] by delegating to the package's
// standard decoder pipeline against the inner T's address. Works for
// any T the decoder accepts.
func (s *Secret[T]) UnmarshalEnv(text string) error {
	return decodeValue(reflect.ValueOf(&s.v).Elem(), text, decodeOpts{})
}

// secretMarker is the redaction string returned by Secret[T]'s output
// methods. It is intentionally distinct from the encoder's "***"
// (Phase 6) so that secret values redacted via the wrapper are
// distinguishable in logs from values redacted via the encoder's
// secret tag.
const secretMarker = "<redacted>"
