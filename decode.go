package env

import (
	"context"
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

// decodeOpts is the per-field tag-driven subset of [fieldDescriptor]
// that affects value decoding. Layout propagates into nested decoding
// (slice elements, map values); Separator, KVSeparator, Base64, and
// Hex apply only at the field level.
type decodeOpts struct {
	Separator   string
	KVSeparator string
	Layout      string
	Base64      bool
	Hex         bool
}

func decodeOptsFromDescriptor(d fieldDescriptor) decodeOpts {
	return decodeOpts{
		Separator:   d.Separator,
		KVSeparator: d.KVSeparator,
		Layout:      d.Layout,
		Base64:      d.Base64,
		Hex:         d.Hex,
	}
}

// Type cache for the special-cased built-ins. Types reached via the
// interface chain (time.Time without layout=, url.URL, net.IP,
// regexp.Regexp, big.*) don't need a precomputed reflect.Type.
//
// regexp.Regexp's pointer receiver implements TextUnmarshaler since
// Go 1.21, so the interface chain handles it for free.
var (
	timeTimeType     = reflect.TypeFor[time.Time]()
	timeDurationType = reflect.TypeFor[time.Duration]()
	timeLocationType = reflect.TypeFor[time.Location]()
	netIPNetType     = reflect.TypeFor[net.IPNet]()
)

// decodeValue decodes text into dst, which must be settable and
// addressable. ctx is forwarded to an [UnmarshalerContext] if dst (or
// its pointee, after auto-init) implements one. Priority chain (high to
// low):
//
//  1. Pointer auto-init: nil pointers are allocated; empty text on a
//     pointer leaves the pointee at its zero value.
//  2. Tag-option overrides (time.Time + layout=, []byte + base64=/hex=).
//  3. [UnmarshalerContext] (UnmarshalEnv(ctx, s)).
//  4. [Unmarshaler] (UnmarshalEnv(s)).
//  5. [encoding.TextUnmarshaler] (UnmarshalText).
//  6. [encoding.BinaryUnmarshaler] (UnmarshalBinary).
//  7. Built-in specials (time.Duration, time.Location, net.IPNet) and
//     []byte/[N]byte default raw form.
//  8. Default reflection (primitives, slices, arrays, maps).
func decodeValue(ctx context.Context, dst reflect.Value, text string, opts decodeOpts) error {
	for dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		if text == "" {
			return nil
		}
		dst = dst.Elem()
	}

	if dst.Type() == timeTimeType && opts.Layout != "" {
		return decodeTimeWithLayout(dst, text, opts.Layout)
	}
	if isByteContainer(dst.Type()) && (opts.Base64 || opts.Hex) {
		return decodeBytes(dst, text, opts)
	}

	addr := dst.Addr()
	if uc, ok := reflect.TypeAssert[UnmarshalerContext](addr); ok {
		if err := uc.UnmarshalEnv(ctx, text); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}
	if u, ok := reflect.TypeAssert[Unmarshaler](addr); ok {
		if err := u.UnmarshalEnv(text); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}
	if tu, ok := reflect.TypeAssert[encoding.TextUnmarshaler](addr); ok {
		if err := tu.UnmarshalText([]byte(text)); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}
	if bu, ok := reflect.TypeAssert[encoding.BinaryUnmarshaler](addr); ok {
		if err := bu.UnmarshalBinary([]byte(text)); err != nil {
			return wrapDecodeErr(dst.Type(), text, err)
		}
		return nil
	}

	if handled, err := decodeBuiltinSpecial(dst, text, opts); handled {
		return err
	}

	return decodeReflect(ctx, dst, text, opts)
}

// decodeBuiltinSpecial handles types the interface chain doesn't reach
// (time.Duration is an int64 alias; net.IPNet has no unmarshaler) plus
// the byte-container default (raw text → bytes).
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

// decodeReflect handles primitive, slice, array, and map kinds.
// Returns [ErrUnsupportedType] for chan, func, complex, interface, and
// struct (which should have been handled by the interface chain). ctx
// propagates to nested element/value decoding.
func decodeReflect(ctx context.Context, dst reflect.Value, text string, opts decodeOpts) error {
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
		return decodeSlice(ctx, dst, text, opts)
	case reflect.Array:
		return decodeArrayNonByte(ctx, dst, text, opts)
	case reflect.Map:
		return decodeMap(ctx, dst, text, opts)
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedType, dst.Type())
}

func decodeTimeWithLayout(dst reflect.Value, text, layout string) error {
	tt, err := time.Parse(layout, text)
	if err != nil {
		return wrapDecodeErr(dst.Type(), text, err)
	}
	dst.Set(reflect.ValueOf(tt))
	return nil
}

// isByteContainer reports whether t is []byte or [N]byte. Strings are
// handled by their own decoder branch, so they are not considered byte
// containers here.
func isByteContainer(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() == reflect.Uint8
	}
	return false
}

// decodeBytes handles []byte and [N]byte fields. Without base64/hex
// the raw text bytes are used; with base64= or hex= the corresponding
// decoder runs. For [N]byte the decoded length must match N.
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

func decodeSlice(ctx context.Context, dst reflect.Value, text string, opts decodeOpts) error {
	sep := separatorOr(opts.Separator, ",")
	var parts []string
	if text != "" {
		parts = strings.Split(text, sep)
	}
	out := reflect.MakeSlice(dst.Type(), len(parts), len(parts))
	elemOpts := decodeOpts{Layout: opts.Layout}
	for i, p := range parts {
		if err := decodeValue(ctx, out.Index(i), p, elemOpts); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

func decodeArrayNonByte(ctx context.Context, dst reflect.Value, text string, opts decodeOpts) error {
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
		if err := decodeValue(ctx, dst.Index(i), p, elemOpts); err != nil {
			return err
		}
	}
	return nil
}

func decodeMap(ctx context.Context, dst reflect.Value, text string, opts decodeOpts) error {
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
		if err := decodeValue(ctx, keyHolder.Elem(), k, decodeOpts{}); err != nil {
			return err
		}
		valHolder := reflect.New(valType)
		if err := decodeValue(ctx, valHolder.Elem(), v, valOpts); err != nil {
			return err
		}
		out.SetMapIndex(keyHolder.Elem(), valHolder.Elem())
	}
	dst.Set(out)
	return nil
}

// Internal decoder sentinels surfaced as [*ParseError] causes; reach
// them via [errors.Is].
var (
	errInvalidBool         = errors.New("invalid boolean")
	errBytesLengthMismatch = errors.New("byte length mismatch")
	errArrayLengthMismatch = errors.New("element count mismatch")
	errMissingKVSeparator  = errors.New("missing kv separator")
)

// parseBool accepts case-insensitive true/false, 1/0, yes/no, on/off.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%w: %q", errInvalidBool, s)
}

// wrapDecodeErr wraps a low-level decode failure as a [*ParseError].
// Field, Key, and Pos are left empty; the loader fills them in.
func wrapDecodeErr(typ reflect.Type, text string, cause error) error {
	return &ParseError{
		Value: text,
		Type:  typ.String(),
		Cause: cause,
	}
}

func separatorOr(sep, fallback string) string {
	if sep == "" {
		return fallback
	}
	return sep
}
