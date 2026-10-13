package env

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
)

// Secret is a generic wrapper that hides T's value in textual output
// and integrates with the standard observability surfaces:
//
//   - [fmt.Formatter], [fmt.Stringer], [fmt.GoStringer]: write the
//     redaction marker for every fmt verb (%v, %#v, %d, %t, ...), never
//     the value, except %p and %w: fmt formats those by reflection
//     without calling Format, so they print the value. go vet reports
//     %w but not %p, since Secret implements [fmt.Formatter]. Don't pass
//     a Secret, or a struct holding one, to %p or %w.
//   - [slog.LogValuer]: emits the marker, so slog attributes redact
//     automatically.
//   - [Marshaler]: returns the underlying T for the reverse [Encode]
//     path. [Encode] writes a Secret field as "***" unless
//     [WithEncodeIncludeSecrets] is set.
//   - [Unmarshaler]: decodes via the standard decoder pipeline; works
//     for any T the decoder accepts.
//
// fmt also cannot reach the methods of a value in an unexported struct
// field, so it prints such a field's contents by reflection. Keep
// Secret fields exported. A Secret[T] is comparable when T is, and two
// such Secrets are == when their values are.
//
// The field's tag options apply to T: base64/hex on a Secret[[]byte],
// layout= on a Secret[time.Time], separator= on a Secret[[]string]. A
// field whose type is Secret[T], or a slice, array or map of
// Secret[T], is treated as secret-tagged everywhere: [Encode],
// [Describe], [*ParseError] and [*ImmutableChangedError] redact it.
// A custom decoder or marshaler registered for T is not applied to a
// Secret[T]: register one for Secret[T] itself.
//
//	type Config struct {
//	    Token env.Secret[string] `env:"TOKEN,required"`
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
// marker.
func (s Secret[T]) GoString() string {
	return secretMarker
}

// Format implements [fmt.Formatter] and writes the redaction marker
// for every verb, honoring width and flags. %q quotes the marker and
// %x/%X hex-encode it, as for a string.
func (s Secret[T]) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q', 'x', 'X':
		_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), secretMarker)
	default:
		_, _ = fmt.Fprintf(f, fmt.FormatString(f, 's'), secretMarker)
	}
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
// decoder against the inner T. Called directly, it applies no tag
// options and gives an [UnmarshalerContext] T context.Background(); the
// loader instead decodes T with the field's options and context.
func (s *Secret[T]) UnmarshalEnv(text string) error {
	return s.decodeSecret(context.Background(), text, decodeOpts{})
}

// secretCodec lets the decoder and encoder reach a Secret's T with the
// field's tag options.
type secretCodec interface {
	decodeSecret(ctx context.Context, text string, opts decodeOpts) error
	renderSecret(e *encoder, fd fieldDescriptor) (string, error)
}

func (s *Secret[T]) decodeSecret(ctx context.Context, text string, opts decodeOpts) error {
	return decodeValue(ctx, reflect.ValueOf(&s.v).Elem(), text, opts)
}

func (s *Secret[T]) renderSecret(e *encoder, fd fieldDescriptor) (string, error) {
	return e.renderUncustomized(reflect.ValueOf(&s.v).Elem(), fd)
}

// secretMarker is intentionally distinct from the encoder's "***" so
// wrapper-redacted values stay distinguishable in logs.
const secretMarker = "<redacted>"

// secretPkgPath is the import path [Secret] instantiations report.
var secretPkgPath = reflect.TypeFor[Secret[struct{}]]().PkgPath()

// isSecret reports whether t is an instantiation of [Secret] itself. A
// struct that embeds a Secret is not one, although Secret's methods are
// promoted to it.
func isSecret(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == secretPkgPath &&
		strings.HasPrefix(t.Name(), "Secret[")
}

// isSecretType reports whether t is a [Secret] instantiation, looking
// through pointers and slice, array and map element (and map key)
// types. Fields of such types are treated as secret-tagged.
func isSecretType(t reflect.Type) bool {
	return secretTypeSeen(t, nil)
}

// secretTypeSeen is isSecretType with a record of the container types
// already visited, so self-referencing types (type Tree map[string]Tree)
// terminate.
func secretTypeSeen(t reflect.Type, seen []reflect.Type) bool {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			if slices.Contains(seen, t) {
				return false
			}
			seen = append(seen, t)
			if t.Kind() == reflect.Map && secretTypeSeen(t.Key(), seen) {
				return true
			}
			t = t.Elem()
			continue
		}
		return isSecret(t)
	}
}
