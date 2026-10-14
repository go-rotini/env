package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
)

// Loader is a configured environment-variable loader. Construct with
// [New]; safe for concurrent use after construction.
type Loader struct {
	opts loaderOptions

	// defaultWalk is set when the tag name and field-name transform are
	// the defaults, so the package-wide field cache applies.
	defaultWalk bool

	// cache maps a struct type to its *loaderFields when this loader
	// cannot use the package-wide cache directly (non-default walk or a
	// prefix).
	cache sync.Map
}

// loaderFields is a loader's view of a struct type: the walked fields
// and each field's key with [WithPrefix] applied.
type loaderFields struct {
	info *structInfo
	keys []string
}

// loadState is the per-load bookkeeping that live reload needs.
type loadState struct {
	// values maps each field's key to the final string it was decoded
	// from (after defaults, mutators, expansion and fromFile), or to
	// [unsetSentinel] when the field had no value.
	values map[string]string

	// retained maps each unset-tagged key removed from the process
	// environment to its value, so later reloads still see it. It is
	// shared across every load of one [Loader.Watch] or [Live].
	retained map[string]string
}

// New returns a [Loader] with opts applied to the defaults: source
// chain of [OSEnv] alone, no prefix, tag name "env", SCREAMING_SNAKE_CASE
// field-name transform, no logger, optional-by-default field policy.
func New(opts ...Option) *Loader {
	o := defaultLoaderOptions()
	for _, opt := range opts {
		opt(&o)
	}
	l := &Loader{opts: o}
	l.defaultWalk = l.usesDefaultWalkConfig()
	return l
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
	return l.load(ctx, v, nil)
}

// load is [Loader.LoadContext] with optional live-reload bookkeeping.
func (l *Loader) load(ctx context.Context, v any, st *loadState) error {
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

	info, keys := l.fields(rv.Type())
	if info.Err != nil {
		return info.Err
	}

	src := l.composedSource()

	multi := &MultiError{}
	for i, fd := range info.Fields {
		l.populateField(ctx, rv, fd, l.key(keys, i, fd), src, multi, st)
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

	info, keys := l.fields(rv.Type())
	if info.Err != nil {
		return nil, info.Err
	}

	src := l.composedSource()
	out := make(map[string]string, len(info.Fields))
	for i, fd := range info.Fields {
		key := l.key(keys, i, fd)
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

// lookupStructInfo returns the walked fields of t for this loader.
func (l *Loader) lookupStructInfo(t reflect.Type) *structInfo {
	info, _ := l.fields(t)
	return info
}

// fields returns the walked fields of t and their prefixed keys. keys
// is nil when the keys need no prefix; use [Loader.key] to read them.
// Loaders with the default walk config share the package-wide cache;
// others walk each type once and cache it on the loader.
func (l *Loader) fields(t reflect.Type) (*structInfo, []string) {
	if l.defaultWalk && l.opts.prefix == "" {
		return getStructInfo(t), nil
	}
	if c, ok := l.cache.Load(t); ok {
		lf, _ := c.(*loaderFields)
		return lf.info, lf.keys
	}
	var info *structInfo
	if l.defaultWalk {
		info = getStructInfo(t)
	} else {
		info = l.collectStructInfoFresh(t)
	}
	keys := make([]string, len(info.Fields))
	for i, fd := range info.Fields {
		keys[i] = l.opts.prefix + fd.Key
	}
	c, _ := l.cache.LoadOrStore(t, &loaderFields{info: info, keys: keys})
	lf, _ := c.(*loaderFields)
	return lf.info, lf.keys
}

// key returns the prefixed key of field i from keys as returned by
// [Loader.fields].
func (*Loader) key(keys []string, i int, fd fieldDescriptor) string {
	if keys == nil {
		return fd.Key
	}
	return keys[i]
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
//  1. Source lookup; for an unset-tagged field, removal of the variable
//     from the process environment (unless [WithoutUnset]).
//  2. Default application (if not sourced).
//  3. Mutator chain (may rewrite or synthesize).
//  4. Required check (when the field has no value at all).
//  5. Variable expansion (`expand` tag).
//  6. notEmpty check.
//  7. fromFile substitution (read file contents as the value).
//  8. Decode: custom decoder, then the value decoder (which checks, in
//     order, [Secret] (its value decoded the same way), layout= and
//     base64/hex, [UnmarshalerContext], [Unmarshaler],
//     [encoding.TextUnmarshaler], [encoding.BinaryUnmarshaler], built-in
//     specials, then reflection).
func (l *Loader) populateField(ctx context.Context, root reflect.Value, fd fieldDescriptor, resolvedKey string, src Source, multi *MultiError, st *loadState) {
	rawValue, sourced := src.Lookup(resolvedKey)
	if fd.Unset && !l.opts.withoutUnset {
		if sourced {
			unsetOS(resolvedKey, rawValue, st)
		} else if v, ok := st.retainedValue(resolvedKey); ok {
			rawValue, sourced = v, true
		}
	}
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
		st.record(resolvedKey, unsetSentinel)
		if fd.Required || fd.NotEmpty || (l.opts.requireAll && !fd.HasDefault) {
			multi.Append(&MissingRequiredError{
				Field: fd.GoPath,
				Key:   resolvedKey,
			})
		}
		return
	}

	if fd.Expand {
		expanded, err := expandFieldValue(rawValue, fd.GoPath, src.Lookup)
		if err != nil {
			if ae, ok := errors.AsType[*assertionError](err); ok && fd.Secret {
				err = &assertionError{goPath: ae.goPath, name: ae.name}
			}
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
			multi.Append(fmt.Errorf("env: field %s: read %q: %w: %w",
				fd.GoPath, rawValue, ErrFromFileRead, err))
			return
		}
		text := string(data)
		text = strings.TrimSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\r")
		rawValue = text
	}

	st.record(resolvedKey, rawValue)

	field, err := fieldByIndexAlloc(root, fd.Index)
	if err != nil {
		multi.Append(fmt.Errorf("env: field %s: %w", fd.GoPath, err))
		return
	}

	if dec, ok := l.opts.customDecoders[field.Type()]; ok {
		if err := dec(rawValue, field); err != nil {
			if _, ok := errors.AsType[*ParseError](err); !ok { //nolint:errcheck // only whether it matches is used
				err = wrapDecodeErr(field.Type(), rawValue, err)
			}
			multi.Append(l.annotateParseError(err, fd, resolvedKey, rawValue, field.Type()))
		}
		return
	}

	if err := decodeValue(ctx, field, rawValue, decodeOptsFromDescriptor(fd)); err != nil {
		multi.Append(l.annotateParseError(err, fd, resolvedKey, rawValue, field.Type()))
	}
}

// unsetOS honors the `unset` tag: it removes key from the process
// environment when the environment holds the value that was loaded, so
// a same-named variable the value did not come from is left alone. The
// removed value is kept in st for later reloads. os.Unsetenv is not
// thread-safe under cgo (POSIX setenv/unsetenv can race with
// getaddrinfo when GODEBUG=netdns=cgo); callers should perform their
// initial Load before spawning goroutines that may trigger cgo lookups.
func unsetOS(key, value string, st *loadState) {
	if v, ok := os.LookupEnv(key); !ok || v != value {
		return
	}
	_ = os.Unsetenv(key)
	if st != nil {
		st.retained[key] = value
	}
}

func (st *loadState) record(key, value string) {
	if st != nil {
		st.values[key] = value
	}
}

func (st *loadState) retainedValue(key string) (string, bool) {
	if st == nil {
		return "", false
	}
	v, ok := st.retained[key]
	return v, ok
}

// fieldByIndexAlloc walks index on root, allocating nil pointers to
// intermediate structs (every pointer level). It fails, rather than
// panicking, when a nil pointer cannot be set: an embedded pointer to
// an unexported struct type.
func fieldByIndexAlloc(root reflect.Value, index []int) (reflect.Value, error) {
	v := root
	for i, idx := range index {
		if i > 0 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					if !v.CanSet() {
						return reflect.Value{}, fmt.Errorf(
							"cannot allocate nil embedded pointer to unexported struct %s: %w",
							v.Type().Elem(), ErrUnsupportedType)
					}
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(idx)
	}
	return v, nil
}

// annotateParseError adds field context to a [*ParseError] returned by
// the decoder, redacting the value (and the cause, since parse errors
// commonly quote the raw value) for secret fields. Non-ParseError
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
