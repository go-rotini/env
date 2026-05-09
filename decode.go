package env

import (
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// decodeOpts carries the per-field tag-driven options that affect value
// decoding. It is a subset of fieldDescriptor: only the options that
// actually change how a string is parsed are propagated.
//
// For nested decoding (slice elements, map keys/values), Layout passes
// through (so a []time.Time can use a custom layout); Separator and
// KVSeparator do NOT propagate (they apply at exactly one container
// level); Base64/Hex do not propagate (they are []byte-specific).
type decodeOpts struct {
	Separator   string
	KVSeparator string
	Layout      string
	Base64      bool
	Hex         bool
}

// decodeOptsFromDescriptor extracts the decoder-relevant options from a
// fieldDescriptor.
func decodeOptsFromDescriptor(d fieldDescriptor) decodeOpts {
	return decodeOpts{
		Separator:   d.Separator,
		KVSeparator: d.KVSeparator,
		Layout:      d.Layout,
		Base64:      d.Base64,
		Hex:         d.Hex,
	}
}

// Type cache for built-in special handling. Only types actually special-
// cased in this file are cached here; types that go through the interface
// chain (time.Time without layout=, url.URL, net.IP, regexp.Regexp,
// big.Int, big.Float, big.Rat) don't need a precomputed reflect.Type.
//
// Specifically: regexp.Regexp's pointer receiver implements TextUnmarshaler
// since Go 1.21, so it is handled by the interface chain even though it is
// nominally a "built-in special". The behavior is equivalent
// (regexp.UnmarshalText calls Compile), so we let the interface chain handle
// it for free.
var (
	timeTimeType     = reflect.TypeFor[time.Time]()
	timeDurationType = reflect.TypeFor[time.Duration]()
	timeLocationType = reflect.TypeFor[time.Location]()
	netIPNetType     = reflect.TypeFor[net.IPNet]()
)

// decodeValue decodes text into dst.
//
// dst must be settable and addressable: typically obtained via
// [reflect.Value.FieldByIndex] on a settable parent struct, or via
// [reflect.New] for transient values used as map keys/values.
//
// The priority chain (high to low):
//
//  1. Pointer auto-init: dst of kind Pointer is allocated as needed and
//     the loop recurses into the pointee. The "set with empty value" case
//     short-circuits with an allocated zero-T pointer (no recursion into T).
//  2. Tag-option overrides: types whose decoding behavior changes based on
//     a tag option (time.Time + layout=, []byte/[N]byte + base64=/hex=).
//     These run BEFORE the interface chain so user tag intent takes
//     precedence over interface defaults.
//  3. [Unmarshaler] (UnmarshalEnv).
//  4. [encoding.TextUnmarshaler] (UnmarshalText).
//  5. [encoding.BinaryUnmarshaler] (UnmarshalBinary).
//  6. Built-in types not covered by interfaces (time.Duration, time.Location,
//     net.IPNet, regexp.Regexp) and []byte/[N]byte default raw-bytes form.
//  7. Default reflection-based decoding for primitives, slices, arrays, maps.
func decodeValue(dst reflect.Value, text string, opts decodeOpts) error {
	// 1. Pointer auto-init.
	for dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		if text == "" {
			// Set-but-empty: leave the pointee at its zero value rather
			// than recursing (which would fail for many leaf types).
			return nil
		}
		dst = dst.Elem()
	}

	// 2. Tag-option overrides.
	if dst.Type() == timeTimeType && opts.Layout != "" {
		return decodeTimeWithLayout(dst, text, opts.Layout)
	}
	if isByteContainer(dst.Type()) && (opts.Base64 || opts.Hex) {
		return decodeBytes(dst, text, opts)
	}

	// 3-5. Interface chain.
	addr := dst.Addr()
	if u, ok := addr.Interface().(Unmarshaler); ok {
		if err := u.UnmarshalEnv(text); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}
	if tu, ok := addr.Interface().(encoding.TextUnmarshaler); ok {
		if err := tu.UnmarshalText([]byte(text)); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}
	if bu, ok := addr.Interface().(encoding.BinaryUnmarshaler); ok {
		if err := bu.UnmarshalBinary([]byte(text)); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}

	// 6. Built-in types not covered by interfaces.
	if handled, err := decodeBuiltinSpecial(dst, text, opts); handled {
		return err
	}

	// 7. Default reflection-based decoding.
	return decodeReflect(dst, text, opts)
}

// decodeBuiltinSpecial handles the set of well-known types that the
// interface chain wouldn't otherwise reach (time.Duration is a sized int
// alias with no UnmarshalText; net.IPNet and regexp.Regexp have no
// unmarshal interfaces) and the byte-container default (raw text → bytes).
func decodeBuiltinSpecial(dst reflect.Value, text string, opts decodeOpts) (bool, error) {
	switch dst.Type() {
	case timeDurationType:
		d, err := time.ParseDuration(text)
		if err != nil {
			return true, wrapDecodeErr(dst.Type(), text, err)
		}
		dst.SetInt(int64(d))
		return true, nil
	case timeLocationType:
		loc, err := time.LoadLocation(text)
		if err != nil {
			return true, wrapDecodeErr(dst.Type(), text, err)
		}
		dst.Set(reflect.ValueOf(loc).Elem())
		return true, nil
	case netIPNetType:
		_, ipnet, err := net.ParseCIDR(text)
		if err != nil {
			return true, wrapDecodeErr(dst.Type(), text, err)
		}
		dst.Set(reflect.ValueOf(*ipnet))
		return true, nil
	}
	if isByteContainer(dst.Type()) {
		return true, decodeBytes(dst, text, opts)
	}
	return false, nil
}

// decodeReflect handles primitive, slice, array, and map kinds via
// reflection. Types unsupported by both the interface chain and the
// built-in registry land here and produce [ErrUnsupportedType] when no
// kind branch matches (chan, func, complex, interface{}, struct).
func decodeReflect(dst reflect.Value, text string, opts decodeOpts) error {
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(text)
		return nil
	case reflect.Bool:
		b, err := parseBool(text)
		if err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		dst.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(text, 10, dst.Type().Bits())
		if err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		dst.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(text, 10, dst.Type().Bits())
		if err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		dst.SetUint(n)
		return nil
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(text, dst.Type().Bits())
		if err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		dst.SetFloat(n)
		return nil
	case reflect.Slice:
		return decodeSlice(dst, text, opts)
	case reflect.Array:
		return decodeArrayNonByte(dst, text, opts)
	case reflect.Map:
		return decodeMap(dst, text, opts)
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedType, dst.Type())
}

// decodeTimeWithLayout parses text as a time.Time using the user-supplied
// layout. Called when the layout= tag option is set on a time.Time field.
func decodeTimeWithLayout(dst reflect.Value, text, layout string) error {
	tt, err := time.Parse(layout, text)
	if err != nil {
		return wrapDecodeErr(dst.Type(), text, err)
	}
	dst.Set(reflect.ValueOf(tt))
	return nil
}

// isByteContainer reports whether t is []byte or [N]byte. Strings are not
// byte containers in this sense (they have their own decoder branch).
func isByteContainer(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() == reflect.Uint8
	}
	return false
}

// decodeBytes handles []byte and [N]byte fields. With no base64/hex tag,
// raw text bytes are used. With base64= or hex=, the corresponding decoder
// runs; for fixed-size [N]byte, the decoded length must match N.
func decodeBytes(dst reflect.Value, text string, opts decodeOpts) error {
	var data []byte
	var err error
	switch {
	case opts.Base64:
		data, err = base64.StdEncoding.DecodeString(text)
	case opts.Hex:
		data, err = hex.DecodeString(text)
	default:
		data = []byte(text)
	}
	if err != nil {
		return wrapDecodeErr(dst.Type(), text, err)
	}

	if dst.Kind() == reflect.Slice {
		dst.SetBytes(data)
		return nil
	}
	// reflect.Array: enforce length.
	if len(data) != dst.Len() {
		return &ParseError{
			Value: text,
			Type:  dst.Type().String(),
			Cause: fmt.Errorf("%w: got %d bytes, want %d", errBytesLengthMismatch, len(data), dst.Len()),
		}
	}
	reflect.Copy(dst, reflect.ValueOf(data))
	return nil
}

// decodeSlice splits text by the configured separator and decodes each
// element. The empty input produces an empty (non-nil) slice. The Layout
// option propagates to elements; Separator/KVSeparator do not.
func decodeSlice(dst reflect.Value, text string, opts decodeOpts) error {
	sep := separatorOr(opts.Separator, ",")
	var parts []string
	if text != "" {
		parts = strings.Split(text, sep)
	}
	out := reflect.MakeSlice(dst.Type(), len(parts), len(parts))
	elemOpts := decodeOpts{Layout: opts.Layout}
	for i, p := range parts {
		if err := decodeValue(out.Index(i), p, elemOpts); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// decodeArrayNonByte decodes a [N]T array where T is not byte. The number
// of split parts must equal N; mismatches return ErrParse. (Byte arrays
// are routed through decodeBytes earlier.)
func decodeArrayNonByte(dst reflect.Value, text string, opts decodeOpts) error {
	sep := separatorOr(opts.Separator, ",")
	var parts []string
	if text != "" {
		parts = strings.Split(text, sep)
	}
	if len(parts) != dst.Len() {
		return &ParseError{
			Value: text,
			Type:  dst.Type().String(),
			Cause: fmt.Errorf("%w: got %d elements, want %d", errArrayLengthMismatch, len(parts), dst.Len()),
		}
	}
	elemOpts := decodeOpts{Layout: opts.Layout}
	for i, p := range parts {
		if err := decodeValue(dst.Index(i), p, elemOpts); err != nil {
			return err
		}
	}
	return nil
}

// decodeMap parses a separator-joined sequence of key/value pairs. Empty
// text produces an empty (non-nil) map. The element decoder is applied
// recursively to keys and values; map keys must therefore be a type the
// decoder accepts.
func decodeMap(dst reflect.Value, text string, opts decodeOpts) error {
	sep := separatorOr(opts.Separator, ",")
	kvSep := separatorOr(opts.KVSeparator, ":")
	if text == "" {
		dst.Set(reflect.MakeMap(dst.Type()))
		return nil
	}
	pairs := strings.Split(text, sep)
	out := reflect.MakeMapWithSize(dst.Type(), len(pairs))
	keyType := dst.Type().Key()
	valType := dst.Type().Elem()
	valOpts := decodeOpts{Layout: opts.Layout}
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, kvSep)
		if !ok {
			return &ParseError{
				Value: text,
				Type:  dst.Type().String(),
				Cause: fmt.Errorf("%w %q in entry %q", errMissingKVSeparator, kvSep, pair),
			}
		}
		keyHolder := reflect.New(keyType)
		if err := decodeValue(keyHolder.Elem(), k, decodeOpts{}); err != nil {
			return err
		}
		valHolder := reflect.New(valType)
		if err := decodeValue(valHolder.Elem(), v, valOpts); err != nil {
			return err
		}
		out.SetMapIndex(keyHolder.Elem(), valHolder.Elem())
	}
	dst.Set(out)
	return nil
}

// Decoder-internal sentinel errors used as wrapped causes. The decoder's
// public surface always reports a [*ParseError]; these sentinels appear
// inside the Cause chain and are reachable via [errors.Is].
var (
	errInvalidBool         = errors.New("invalid boolean")
	errBytesLengthMismatch = errors.New("byte length mismatch")
	errArrayLengthMismatch = errors.New("element count mismatch")
	errMissingKVSeparator  = errors.New("missing kv separator")
)

// parseBool implements a tolerant bool parser: case-insensitive
// true/false plus 1/0, yes/no, on/off.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%w: %q", errInvalidBool, s)
}

// wrapDecodeErr wraps a low-level decode failure as a [*ParseError]. The
// Field, Key, and Pos fields are left empty; the loader fills them in
// after the decoder returns.
func wrapDecodeErr(typ reflect.Type, text string, cause error) error {
	return &ParseError{
		Value: text,
		Type:  typ.String(),
		Cause: cause,
	}
}

// separatorOr returns sep when non-empty, else fallback.
func separatorOr(sep, fallback string) string {
	if sep == "" {
		return fallback
	}
	return sep
}
