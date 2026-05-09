package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// Loader is a configured environment-variable loader. Construct with [New];
// safe for concurrent use after construction.
type Loader struct {
	opts loaderOptions
}

// New returns a [Loader] with the given options applied to the defaults.
//
// Defaults: source chain = [[OSEnv]], no prefix, tag name "env",
// SCREAMING_SNAKE_CASE field-name transform, no logger, POSIX-strict
// identifier rules, optional-by-default field policy.
func New(opts ...Option) *Loader {
	o := defaultLoaderOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return &Loader{opts: o}
}

// Load decodes environment variables (or other source values) into v.
//
// v must be a non-nil pointer to a struct. Per-field errors are
// aggregated into a [*MultiError]; if any field fails, Load returns
// the aggregated error after attempting every field.
//
// Field-collection errors (conflicting names, unknown tag options) are
// returned directly without wrapping in MultiError, since they are
// permanent for the target type.
func (l *Loader) Load(v any) error {
	return l.LoadContext(context.Background(), v)
}

// LoadContext is like [Loader.Load] but accepts a context.
//
// In Phase 5 the context is not yet consumed; Phase 7 wires it into the
// Mutator and Validator hooks, and Phase 11 wires it into Live[T]
// reload propagation.
func (l *Loader) LoadContext(ctx context.Context, v any) error {
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

	// Whole-struct validator hook. Skipped when per-field errors exist:
	// the struct is in a partial state and Validate would likely produce
	// noise that compounds with the parse errors.
	if len(multi.Errors) == 0 {
		if v, ok := rv.Addr().Interface().(Validator); ok {
			if err := v.Validate(); err != nil {
				multi.Append(&ValidationError{Cause: err})
			}
		}
	}

	if len(multi.Errors) > 0 {
		return multi
	}
	return nil
}

// Snapshot captures the current resolved values for every recognized
// field of v as a map[string]string keyed by the resolved env-var name
// (with [WithPrefix] applied). Defaults from default= tags are emitted
// for keys not present in the source chain. Fields whose Source returns
// ok=false and which have no default are omitted.
//
// Useful for tests and debugging: pass the result to [Map] to construct
// a Source that mirrors the live snapshot.
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

// structInfo returns the cached or freshly-walked structInfo for t,
// honoring the loader's tag-name and field-name-transform options.
//
// The Phase 3 cache (defaultStructCache via getStructInfo) is keyed by
// reflect.Type alone and assumes the default config. When the loader
// uses non-default options, we re-walk on every call.
func (l *Loader) structInfo(t reflect.Type) (*structInfo, error) {
	info := l.lookupStructInfo(t)
	if info.Err != nil {
		return nil, info.Err
	}
	return info, nil
}

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

// collectStructInfoFresh re-walks t with this loader's tag-name and
// field-name-transform options. Returned info is NOT cached.
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

// composedSource returns the loader's effective Source chain as a single
// Source. Empty chains return a no-op Source.
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
//  4. Required / notEmpty checks (after mutator so synthesized values
//     can satisfy required).
//  5. fromFile substitution (read file contents as the actual value).
//  6. Decode (custom decoder or built-in).
//  7. unset (call os.Unsetenv on the resolved key, unless [WithoutUnset]).
//
// (Phase 9) Variable expansion is a step between the mutator chain and
// fromFile when fd.Expand is set. (Phase 11) Immutable enforcement
// happens at the Live[T] level, not here.
func (l *Loader) populateField(ctx context.Context, root reflect.Value, fd fieldDescriptor, src Source, multi *MultiError) {
	resolvedKey := l.opts.prefix + fd.Key

	rawValue, sourced := src.Lookup(resolvedKey)
	originalValue := rawValue // captured for the mutator chain (always the source's view)
	hasValue := sourced

	if !sourced && fd.HasDefault {
		rawValue = fd.DefaultValue
		hasValue = true
	}

	// Mutator chain runs after default application but before checks /
	// fromFile / decode. A mutator may rewrite the current value or
	// synthesize a value for an otherwise-unset field.
	for _, m := range l.opts.mutators {
		nv, stop, err := m.MutateEnv(ctx, fd.Key, resolvedKey, originalValue, rawValue, sourced)
		if err != nil {
			multi.Append(fmt.Errorf("env: field %s mutator: %w", fd.GoPath, err))
			return
		}
		rawValue = nv
		if !hasValue && nv != "" {
			// Mutator synthesized a value for an unset/no-default field.
			hasValue = true
		}
		if stop {
			break
		}
	}

	// Required / requireAll checks run AFTER the mutator chain so a
	// mutator can satisfy them by synthesizing a value.
	if !hasValue {
		if fd.Required || fd.NotEmpty || (l.opts.requireAll && !fd.HasDefault) {
			multi.Append(&MissingRequiredError{
				Field: fd.GoPath,
				Key:   resolvedKey,
			})
			return
		}
		// Unset and optional: leave the field at its zero value.
		return
	}

	if fd.NotEmpty && rawValue == "" {
		multi.Append(&EmptyValueError{
			Field: fd.GoPath,
			Key:   resolvedKey,
		})
		return
	}

	// fromFile: treat the resolved value as a path; read its contents.
	// A single trailing \n or \r\n is stripped, matching the convention
	// used by `kubectl create secret --from-file`.
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

	if err := decodeValue(field, rawValue, decodeOptsFromDescriptor(fd)); err != nil {
		multi.Append(l.annotateParseError(err, fd, resolvedKey, rawValue, field.Type()))
		return
	}

	l.maybeUnset(fd, resolvedKey)
}

// maybeUnset honors the unset tag option after a successful decode. The
// global [WithoutUnset] disables the behavior. Calls os.Unsetenv with
// the inherent thread-safety caveat documented at §12.14: callers using
// the unset tag accept that the loader does an os-level write.
func (l *Loader) maybeUnset(fd fieldDescriptor, resolvedKey string) {
	if !fd.Unset || l.opts.withoutUnset {
		return
	}
	_ = os.Unsetenv(resolvedKey)
}

// fieldByIndexAlloc walks the FieldByIndex path on root, allocating any
// nil intermediate pointer-to-struct fields along the way.
//
// Stdlib's [reflect.Value.FieldByIndex] panics on a nil intermediate
// pointer; we explicitly auto-allocate. The Loader's preconditions
// (non-nil pointer to struct) guarantee root is settable, which in turn
// guarantees every descendant field is settable, so allocation always
// succeeds; no error path is needed.
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

// annotateParseError augments a [*ParseError] returned from the decoder
// with field-level context, redacting the value if the field is tagged
// secret. Other error types are returned unchanged.
func (l *Loader) annotateParseError(err error, fd fieldDescriptor, key, value string, typ reflect.Type) error {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err
	}
	// Build a copy to avoid mutating an error owned by the decoder
	// (which may be a sentinel or shared with another goroutine).
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
		// Wipe the underlying cause too: stdlib parse errors typically
		// include the raw value in their Error() text (strconv prefixes
		// "parsing %q"), which would leak the secret. We trade a tiny
		// bit of diagnostic detail for guaranteed redaction.
		out.Cause = nil
	}
	return &out
}

// redactor returns the loader's secret redactor, or the package default
// if the loader was constructed before Phase 7 (defensive — every Loader
// constructed via [New] has the default set).
func (l *Loader) redactor() func(string) string {
	if l.opts.secretRedactor != nil {
		return l.opts.secretRedactor
	}
	return defaultSecretRedactor
}
