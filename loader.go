package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// Loader is a configured environment-variable loader. Construct with
// [New]; safe for concurrent use after construction.
type Loader struct {
	opts loaderOptions
}

// New returns a [Loader] with opts applied to the defaults: source
// chain of [OSEnv] alone, no prefix, tag name "env", SCREAMING_SNAKE_CASE
// field-name transform, no logger, optional-by-default field policy.
func New(opts ...Option) *Loader {
	o := defaultLoaderOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return &Loader{opts: o}
}

// Load decodes the configured sources into v. v must be a non-nil
// pointer to a struct. Per-field errors aggregate into a [*MultiError]
// after every field is attempted; field-collection errors (conflicting
// names, unknown tag options) are returned directly.
func (l *Loader) Load(v any) error {
	return l.LoadContext(context.Background(), v)
}

// LoadContext is like [Loader.Load] but accepts a context. ctx is
// forwarded to the [Mutator] chain, to fields whose type implements
// [UnmarshalerContext], and to a [ValidatorContext] hook on the target
// struct (if it implements one). ctx must not be nil.
func (l *Loader) LoadContext(ctx context.Context, v any) error {
	if ctx == nil {
		return ErrNilContext
	}
	if v == nil {
		return ErrNilPointer
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("got %T: %w", v, ErrNilPointer)
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("got %T: %w", v, ErrNotStruct)
	}

	info, err := l.structInfo(rv.Type())
	if err != nil {
		return err
	}

	src := l.composedSource()

	multi := &MultiError{}
	for _, fd := range info.Fields {
		l.populateField(ctx, rv, fd, src, multi)
	}

	// Skip whole-struct validation when per-field errors exist; the
	// struct is in a partial state and Validate would amplify noise.
	// ValidatorContext is preferred over Validator when both are present.
	if len(multi.Errors) == 0 {
		var verr error
		switch v := rv.Addr().Interface().(type) {
		case ValidatorContext:
			verr = v.Validate(ctx)
		case Validator:
			verr = v.Validate()
		}
		if verr != nil {
			multi.Append(&ValidationError{Cause: verr})
		}
	}

	if len(multi.Errors) > 0 {
		return multi
	}
	return nil
}

// Snapshot returns, for every recognized field of v, the raw value the
// loader would start from: the source-chain value if present, otherwise
// the field's default= value; fields with neither are omitted. Keys
// carry [WithPrefix]. Mutators, the `expand` tag, `fromFile`, and
// decoding are NOT applied — Snapshot is the "what the sources say"
// view, useful for tests and debugging (feed it back to [Map]).
func (l *Loader) Snapshot(v any) (map[string]string, error) {
	if v == nil {
		return nil, ErrNilPointer
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("got %T: %w", v, ErrNilPointer)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("got %T: %w", v, ErrNotStruct)
	}

	info, err := l.structInfo(rv.Type())
	if err != nil {
		return nil, err
	}

	src := l.composedSource()
	out := make(map[string]string, len(info.Fields))
	for _, fd := range info.Fields {
		key := l.opts.prefix + fd.Key
		if val, ok := src.Lookup(key); ok {
			out[key] = val
		} else if fd.HasDefault {
			out[key] = fd.DefaultValue
		}
	}
	return out, nil
}

func (l *Loader) structInfo(t reflect.Type) (*structInfo, error) {
	info := l.lookupStructInfo(t)
	if info.Err != nil {
		return nil, info.Err
	}
	return info, nil
}

// lookupStructInfo serves from the package-wide cache when the loader
// runs with default tag-walk config; non-default loaders bypass the
// cache and re-walk per call.
func (l *Loader) lookupStructInfo(t reflect.Type) *structInfo {
	if l.usesDefaultWalkConfig() {
		return getStructInfo(t)
	}
	return l.collectStructInfoFresh(t)
}

func (l *Loader) usesDefaultWalkConfig() bool {
	if l.opts.tagName != "env" {
		return false
	}
	if reflect.ValueOf(l.opts.fieldNameXform).Pointer() != reflect.ValueOf(toScreamingSnake).Pointer() {
		return false
	}
	return true
}

func (l *Loader) collectStructInfoFresh(t reflect.Type) *structInfo {
	if t.Kind() != reflect.Struct {
		return &structInfo{
			Err: fmt.Errorf("got %s: %w", t, ErrNotStruct),
		}
	}
	w := walker{
		seen:           make(map[string]string),
		tagName:        l.opts.tagName,
		fieldNameXform: l.opts.fieldNameXform,
	}
	w.walk(t, nil, "", "")
	return &structInfo{
		Fields: w.fields,
		Err:    errors.Join(w.errs...),
	}
}

func (l *Loader) composedSource() Source {
	switch len(l.opts.sources) {
	case 0:
		return emptySource{}
	case 1:
		return l.opts.sources[0]
	}
	return Multi(l.opts.sources...)
}

// populateField runs the per-field pipeline:
//
//  1. Source lookup.
//  2. Default application (if not sourced).
//  3. Mutator chain (may rewrite or synthesize).
//  4. Required check (when the field has no value at all).
//  5. Variable expansion (`expand` tag).
//  6. notEmpty check.
//  7. fromFile substitution (read file contents as the value).
//  8. Decode: custom decoder, then the value decoder (which checks, in
//     order, [UnmarshalerContext], [Unmarshaler], [encoding.TextUnmarshaler],
//     [encoding.BinaryUnmarshaler], built-in specials, then reflection).
//  9. unset (call os.Unsetenv unless [WithoutUnset]).
func (l *Loader) populateField(ctx context.Context, root reflect.Value, fd fieldDescriptor, src Source, multi *MultiError) {
	resolvedKey := l.opts.prefix + fd.Key

	rawValue, sourced := src.Lookup(resolvedKey)
	originalValue := rawValue
	hasValue := sourced

	if !sourced && fd.HasDefault {
		rawValue = fd.DefaultValue
		hasValue = true
	}

	for _, m := range l.opts.mutators {
		nv, stop, err := m.MutateEnv(ctx, fd.Key, resolvedKey, originalValue, rawValue, sourced)
		if err != nil {
			multi.Append(fmt.Errorf("env: field %s mutator: %w", fd.GoPath, err))
			return
		}
		rawValue = nv
		if !hasValue && nv != "" {
			hasValue = true
		}
		if stop {
			break
		}
	}

	if !hasValue {
		if fd.Required || fd.NotEmpty || (l.opts.requireAll && !fd.HasDefault) {
			multi.Append(&MissingRequiredError{
				Field: fd.GoPath,
				Key:   resolvedKey,
			})
			return
		}
		return
	}

	if fd.Expand {
		expanded, err := expandFieldValue(rawValue, fd.GoPath, src.Lookup)
		if err != nil {
			multi.Append(err)
			return
		}
		rawValue = expanded
	}

	if fd.NotEmpty && rawValue == "" {
		multi.Append(&EmptyValueError{
			Field: fd.GoPath,
			Key:   resolvedKey,
		})
		return
	}

	// fromFile: trim a single trailing \n or \r\n, matching how
	// `kubectl create secret --from-file` round-trips.
	if fd.FromFile && rawValue != "" {
		data, err := os.ReadFile(rawValue)
		if err != nil {
			multi.Append(fmt.Errorf("env: field %s: read %q: %w",
				fd.GoPath, rawValue, ErrFromFileRead))
			return
		}
		text := string(data)
		text = strings.TrimSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\r")
		rawValue = text
	}

	field := fieldByIndexAlloc(root, fd.Index)

	if dec, ok := l.opts.customDecoders[field.Type()]; ok {
		if err := dec(rawValue, field); err != nil {
			multi.Append(l.annotateParseError(err, fd, resolvedKey, rawValue, field.Type()))
			return
		}
		l.maybeUnset(fd, resolvedKey)
		return
	}

	if err := decodeValue(ctx, field, rawValue, decodeOptsFromDescriptor(fd)); err != nil {
		multi.Append(l.annotateParseError(err, fd, resolvedKey, rawValue, field.Type()))
		return
	}

	l.maybeUnset(fd, resolvedKey)
}

// maybeUnset honors the `unset` tag after a successful decode (no-op
// under [WithoutUnset]). os.Unsetenv is not thread-safe under cgo
// (POSIX setenv/unsetenv can race with getaddrinfo when
// GODEBUG=netdns=cgo); the loader serializes calls within a single
// Load. Callers should perform their initial Load before spawning
// goroutines that may trigger cgo lookups.
func (l *Loader) maybeUnset(fd fieldDescriptor, resolvedKey string) {
	if !fd.Unset || l.opts.withoutUnset {
		return
	}
	_ = os.Unsetenv(resolvedKey)
}

// fieldByIndexAlloc walks index on root, allocating nil intermediate
// pointer-to-struct fields. Avoids the panic from
// [reflect.Value.FieldByIndex] on nil intermediates. Loader
// preconditions guarantee every descendant is settable, so allocation
// always succeeds.
func fieldByIndexAlloc(root reflect.Value, index []int) reflect.Value {
	v := root
	for i, idx := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(idx)
	}
	return v
}

// annotateParseError adds field context to a [*ParseError] returned by
// the decoder, redacting the value (and the cause, since stdlib parse
// errors quote the raw value) for secret-tagged fields. Non-ParseError
// returns are passed through.
func (l *Loader) annotateParseError(err error, fd fieldDescriptor, key, value string, typ reflect.Type) error {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err
	}
	// Copy to avoid mutating a sentinel or shared error.
	out := *pe
	out.Field = fd.GoPath
	out.Key = key
	if out.Type == "" {
		out.Type = typ.String()
	}
	if out.Value == "" {
		out.Value = value
	}
	if fd.Secret {
		out.Value = l.redactor()(out.Value)
		out.Cause = nil
	}
	return &out
}

func (l *Loader) redactor() func(string) string {
	if l.opts.secretRedactor != nil {
		return l.opts.secretRedactor
	}
	return defaultSecretRedactor
}
