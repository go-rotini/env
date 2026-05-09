package env

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

// populateField runs the per-field pipeline: source lookup, default
// application, required/notEmpty checks, decode, and assignment.
//
// The Mutator chain (Phase 7), Validator hook (Phase 7), Expand option
// (Phase 9), FromFile / Unset / Secret tag options (Phase 7), and
// Immutable enforcement (Phase 11) are not yet wired; their hook points
// are noted in comments where they would slot into the pipeline.
func (l *Loader) populateField(_ context.Context, root reflect.Value, fd fieldDescriptor, src Source, multi *MultiError) {
	resolvedKey := l.opts.prefix + fd.Key

	rawValue, ok := src.Lookup(resolvedKey)
	if !ok {
		switch {
		case fd.HasDefault:
			rawValue = fd.DefaultValue
		case fd.Required || fd.NotEmpty || (l.opts.requireAll && !fd.HasDefault):
			multi.Append(&MissingRequiredError{
				Field: fd.GoPath,
				Key:   resolvedKey,
			})
			return
		default:
			// Unset and optional: leave the field at its zero value.
			return
		}
	}

	if fd.NotEmpty && rawValue == "" {
		multi.Append(&EmptyValueError{
			Field: fd.GoPath,
			Key:   resolvedKey,
		})
		return
	}

	// (Phase 7) Mutator chain would run here.
	// (Phase 9) Expand pass would run here when fd.Expand is set.
	// (Phase 7) FromFile substitution would run here when fd.FromFile is set.

	field := fieldByIndexAlloc(root, fd.Index)

	if dec, ok := l.opts.customDecoders[field.Type()]; ok {
		if err := dec(rawValue, field); err != nil {
			multi.Append(annotateParseError(err, fd.GoPath, resolvedKey, rawValue, field.Type()))
			return
		}
		// (Phase 7) Unset of fd.Unset would run here.
		return
	}

	if err := decodeValue(field, rawValue, decodeOptsFromDescriptor(fd)); err != nil {
		multi.Append(annotateParseError(err, fd.GoPath, resolvedKey, rawValue, field.Type()))
		return
	}

	// (Phase 7) Unset of fd.Unset would run here.
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
// with field-level context. Other error types are returned unchanged.
func annotateParseError(err error, field, key, value string, typ reflect.Type) error {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err
	}
	// Build a copy to avoid mutating an error owned by the decoder
	// (which may be a sentinel or shared with another goroutine).
	out := *pe
	out.Field = field
	out.Key = key
	if out.Type == "" {
		out.Type = typ.String()
	}
	if out.Value == "" {
		out.Value = value
	}
	return &out
}
