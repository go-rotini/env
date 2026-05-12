package env

import (
	"bufio"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// EncodeOption configures [Encode] and [EncodeFile].
type EncodeOption func(*encoderOptions)

type encoderOptions struct {
	quoteAll         bool
	export           bool
	includeSecrets   bool
	comments         map[string]string
	sortKeys         bool
	customMarshalers map[reflect.Type]customMarshalerFn
}

type customMarshalerFn func(reflect.Value) (string, error)

func defaultEncodeOptions() encoderOptions {
	return encoderOptions{}
}

// WithEncodeQuoteAll forces every value to be double-quoted. The default
// emits values bare when they contain no characters that require
// quoting.
func WithEncodeQuoteAll(b bool) EncodeOption {
	return func(o *encoderOptions) { o.quoteAll = b }
}

// WithEncodeExport prepends `export ` to each emitted line, producing
// output sourceable by Bash.
func WithEncodeExport(b bool) EncodeOption {
	return func(o *encoderOptions) { o.export = b }
}

// WithEncodeIncludeSecrets emits the underlying value of secret-tagged
// fields instead of the `***` marker. Use only when the destination is
// known to be safe (e.g., piping to a child process via inherited env,
// not when writing to a public file).
func WithEncodeIncludeSecrets(b bool) EncodeOption {
	return func(o *encoderOptions) { o.includeSecrets = b }
}

// WithEncodeComments registers per-key comment overrides. Map keys are
// resolved env-var names; values are emitted as `# <text>` lines
// preceding the assignment, replacing any envDesc text.
func WithEncodeComments(c map[string]string) EncodeOption {
	return func(o *encoderOptions) { o.comments = c }
}

// WithEncodeSort emits keys in lexicographic order; the default is
// declaration order.
func WithEncodeSort(b bool) EncodeOption {
	return func(o *encoderOptions) { o.sortKeys = b }
}

// WithCustomMarshaler registers a custom marshaler for type T. Runs at
// the top of the encoder priority chain, ahead of [Marshaler],
// [encoding.TextMarshaler], [encoding.BinaryMarshaler], and the
// built-in registry.
func WithCustomMarshaler[T any](fn func(T) (string, error)) EncodeOption {
	return func(o *encoderOptions) {
		if o.customMarshalers == nil {
			o.customMarshalers = make(map[reflect.Type]customMarshalerFn)
		}
		typ := reflect.TypeFor[T]()
		o.customMarshalers[typ] = func(v reflect.Value) (string, error) {
			t, _ := v.Interface().(T)
			return fn(t)
		}
	}
}

// Encode writes v to w as a `.env`-format byte stream. Each exported,
// env-tagged field becomes one line: `KEY=VALUE`. Nil pointer fields
// and nil slice/map fields are omitted.
//
// v may be a struct value or a non-nil pointer to one. Output is
// sourceable by Bash (with [WithEncodeExport]) and re-parseable by the
// github.com/go-rotini/dotenv parser.
func Encode(w io.Writer, v any, opts ...EncodeOption) error {
	if v == nil {
		return ErrNilPointer
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return fmt.Errorf("got %T: %w", v, ErrNilPointer)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("got %T: %w", v, ErrNotStruct)
	}

	// Marshaler interface checks frequently require pointer receivers,
	// so addressable storage is required.
	if !rv.CanAddr() {
		holder := reflect.New(rv.Type())
		holder.Elem().Set(rv)
		rv = holder.Elem()
	}

	info := getStructInfo(rv.Type())
	if info.Err != nil {
		return info.Err
	}

	o := defaultEncodeOptions()
	for _, opt := range opts {
		opt(&o)
	}

	bw := bufio.NewWriter(w)
	enc := &encoder{w: bw, opts: o}

	fields := info.Fields
	if o.sortKeys {
		fields = slices.Clone(fields)
		slices.SortFunc(fields, func(a, b fieldDescriptor) int {
			return strings.Compare(a.Key, b.Key)
		})
	}

	for _, fd := range fields {
		if err := enc.encodeField(rv, fd); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("env: flush: %w", err)
	}
	return nil
}

// EncodeFile encodes v to path with mode 0600 (owner-only — these
// files often hold secrets). Existing files are truncated.
func EncodeFile(path string, v any, opts ...EncodeOption) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("env: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := Encode(f, v, opts...); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("env: close %s: %w", path, err)
	}
	return nil
}

type encoder struct {
	w    *bufio.Writer
	opts encoderOptions
}

func (e *encoder) encodeField(root reflect.Value, fd fieldDescriptor) error {
	field, ok := fieldByIndexNoAlloc(root, fd.Index)
	if !ok {
		// Unreachable leaf via a nil intermediate pointer-to-struct.
		return nil
	}

	for field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return nil
		}
		field = field.Elem()
	}

	// nil slice / nil map maps to "unset" on round-trip; omit.
	switch field.Kind() {
	case reflect.Slice, reflect.Map:
		if field.IsNil() {
			return nil
		}
	}

	var (
		text string
		err  error
	)
	if fd.Secret && !e.opts.includeSecrets {
		text = "***"
	} else {
		text, err = e.renderValue(field, fd)
		if err != nil {
			return err
		}
	}

	desc := fd.Description
	if d, ok := e.opts.comments[fd.Key]; ok {
		desc = d
	}
	if desc != "" {
		for line := range strings.SplitSeq(desc, "\n") {
			if _, err := fmt.Fprintf(e.w, "# %s\n", line); err != nil {
				return fmt.Errorf("env: write comment: %w", err)
			}
		}
	}

	if e.opts.export {
		if _, err := e.w.WriteString("export "); err != nil {
			return err
		}
	}
	if _, err := e.w.WriteString(fd.Key); err != nil {
		return err
	}
	if err := e.w.WriteByte('='); err != nil {
		return err
	}
	if _, err := e.w.WriteString(formatValue(text, e.opts.quoteAll)); err != nil {
		return err
	}
	return e.w.WriteByte('\n')
}

// renderValue produces the string form of field. Priority chain
// (high to low):
//
//  1. Custom marshaler from [WithCustomMarshaler].
//  2. Tag overrides: time.Time + layout= → t.Format(layout); []byte /
//     [N]byte + base64=/hex= → encoded form.
//  3. [Marshaler] (MarshalEnv).
//  4. [encoding.TextMarshaler].
//  5. [encoding.BinaryMarshaler].
//  6. Built-in specials (time.Duration, time.Location, net.IPNet).
//  7. Byte-container raw form.
//  8. Default reflection.
func (e *encoder) renderValue(field reflect.Value, fd fieldDescriptor) (string, error) {
	typ := field.Type()

	if fn, ok := e.opts.customMarshalers[typ]; ok {
		return fn(field)
	}

	if typ == timeTimeType && fd.Layout != "" {
		t, _ := field.Interface().(time.Time)
		return t.Format(fd.Layout), nil
	}
	if isByteContainer(typ) && (fd.Base64 || fd.Hex) {
		return e.renderBytes(field, fd)
	}

	if field.CanAddr() {
		addr := field.Addr()
		if m, ok := addr.Interface().(Marshaler); ok {
			s, err := m.MarshalEnv()
			if err != nil {
				return "", fmt.Errorf("env: %s.MarshalEnv: %w", typ, err)
			}
			return s, nil
		}
		if m, ok := addr.Interface().(encoding.TextMarshaler); ok {
			data, err := m.MarshalText()
			if err != nil {
				return "", fmt.Errorf("env: %s.MarshalText: %w", typ, err)
			}
			return string(data), nil
		}
		if m, ok := addr.Interface().(encoding.BinaryMarshaler); ok {
			data, err := m.MarshalBinary()
			if err != nil {
				return "", fmt.Errorf("env: %s.MarshalBinary: %w", typ, err)
			}
			return string(data), nil
		}
	}

	switch typ {
	case timeDurationType:
		d, _ := field.Interface().(time.Duration)
		return d.String(), nil
	case timeLocationType:
		loc, _ := field.Interface().(time.Location)
		return loc.String(), nil
	case netIPNetType:
		n, _ := field.Interface().(net.IPNet)
		return n.String(), nil
	}

	if isByteContainer(typ) {
		return e.renderBytes(field, fd)
	}

	return e.renderReflect(field, fd)
}

func (e *encoder) renderBytes(field reflect.Value, fd fieldDescriptor) (string, error) {
	var data []byte
	if field.Kind() == reflect.Slice {
		data = field.Bytes()
	} else {
		data = make([]byte, field.Len())
		reflect.Copy(reflect.ValueOf(data), field)
	}
	switch {
	case fd.Base64:
		return base64.StdEncoding.EncodeToString(data), nil
	case fd.Hex:
		return hex.EncodeToString(data), nil
	}
	return string(data), nil
}

func (e *encoder) renderReflect(field reflect.Value, fd fieldDescriptor) (string, error) {
	switch field.Kind() {
	case reflect.String:
		return field.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(field.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(field.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(field.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(field.Float(), 'g', -1, field.Type().Bits()), nil
	case reflect.Slice:
		return e.renderSliceOrArray(field, fd)
	case reflect.Array:
		return e.renderSliceOrArray(field, fd)
	case reflect.Map:
		return e.renderMap(field, fd)
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedType, field.Type())
}

func (e *encoder) renderSliceOrArray(field reflect.Value, fd fieldDescriptor) (string, error) {
	sep := separatorOr(fd.Separator, ",")
	parts := make([]string, field.Len())
	elemFD := fieldDescriptor{Layout: fd.Layout}
	for i := range field.Len() {
		elem := field.Index(i)
		s, err := e.renderValue(elem, elemFD)
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return strings.Join(parts, sep), nil
}

// renderMap renders a map as separator-joined KV pairs, sorted
// lexicographically. Map values aren't addressable, so transient
// holders give renderValue's interface chain something to take .Addr() on.
func (e *encoder) renderMap(field reflect.Value, fd fieldDescriptor) (string, error) {
	sep := separatorOr(fd.Separator, ",")
	kvSep := separatorOr(fd.KVSeparator, ":")
	keys := field.MapKeys()
	rendered := make([]string, 0, len(keys))
	emptyFD := fieldDescriptor{}
	valueFD := fieldDescriptor{Layout: fd.Layout}
	for _, k := range keys {
		kHolder := reflect.New(k.Type())
		kHolder.Elem().Set(k)
		kStr, err := e.renderValue(kHolder.Elem(), emptyFD)
		if err != nil {
			return "", err
		}
		v := field.MapIndex(k)
		vHolder := reflect.New(v.Type())
		vHolder.Elem().Set(v)
		vStr, err := e.renderValue(vHolder.Elem(), valueFD)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, kStr+kvSep+vStr)
	}
	slices.Sort(rendered)
	return strings.Join(rendered, sep), nil
}

func formatValue(s string, quoteAll bool) string {
	if !quoteAll && !needsQuoting(s) {
		return s
	}
	return quoteValue(s)
}

// needsQuoting reports whether s contains a byte that would be
// misinterpreted in a bare .env value: whitespace, `#`, `$`, quotes,
// backslash, backtick, or any control byte. Iteration is byte-wise —
// not rune-wise — so values containing invalid UTF-8 are classified by
// their actual bytes (and round-trip unchanged).
func needsQuoting(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		case '#', '$', '"', '\'', '\\', '`':
			return true
		default:
			if c < 0x20 || c == 0x7F {
				return true
			}
		}
	}
	return false
}

// quoteValue double-quotes s with the standard escape table applied.
// The result is parseable by github.com/go-rotini/dotenv and by
// joho/godotenv. Iteration is byte-wise so every byte — including bytes
// that are part of invalid UTF-8 — is emitted verbatim (or escaped)
// rather than replaced.
func quoteValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '$':
			b.WriteString(`\$`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// fieldByIndexNoAlloc walks index on root without allocating
// intermediate nil pointer-to-struct fields. Returns ok=false if any
// intermediate is nil so the encoder can omit the unreachable leaf.
func fieldByIndexNoAlloc(root reflect.Value, index []int) (reflect.Value, bool) {
	v := root
	for i, idx := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(idx)
	}
	return v, true
}
