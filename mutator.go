package env

import "context"

// Mutator observes or rewrites resolved values before they reach the
// decoder. It runs once per field per Load, after source lookup and
// default application but before decoding.
//
// Common uses:
//   - Resolving indirections (e.g., a value of "secret://path" rewrites
//     to the actual secret fetched from a vault).
//   - Synthesizing values for unset fields.
//   - Observing values for logging or metrics.
//
// The chain runs in registration order. Returning stop=true halts the
// chain and feeds newValue directly to the decoder. Returning a non-nil
// error terminates the load and adds the error to the [*MultiError].
//
// Mutators do not run for fields that the source-lookup phase did not
// consider (env:"-" fields). They DO run for unset fields with no
// default — the mutator may synthesize a value.
type Mutator interface {
	// MutateEnv inspects or rewrites the value the loader is about to
	// decode.
	//
	//   ctx          : the Load call's context (see [Loader.LoadContext])
	//   originalKey  : the field's tag-given key, before [WithPrefix]
	//   resolvedKey  : the full key after [WithPrefix] and envPrefix
	//   originalValue: the value originally returned by the source
	//                  (or "" with sourced=false if the source had no value).
	//                  Defaults are NOT included here; this is the source's
	//                  view exclusively.
	//   currentValue : the value as it stands after default application
	//                  and any earlier mutators in the chain.
	//   sourced      : false if no Source returned ok=true for this key.
	//                  A mutator returning a non-empty newValue when
	//                  sourced=false acts as if it synthesized the value.
	//
	// Returning stop=true halts the chain immediately and feeds newValue
	// to the decoder. Returning a non-nil error terminates the per-field
	// pipeline.
	MutateEnv(
		ctx context.Context,
		originalKey, resolvedKey, originalValue, currentValue string,
		sourced bool,
	) (newValue string, stop bool, err error)
}

// MutatorFunc adapts a function into a [Mutator]. Useful for small
// inline mutators that don't warrant a named type.
type MutatorFunc func(
	ctx context.Context,
	originalKey, resolvedKey, originalValue, currentValue string,
	sourced bool,
) (string, bool, error)

// MutateEnv implements [Mutator].
func (f MutatorFunc) MutateEnv(
	ctx context.Context,
	originalKey, resolvedKey, originalValue, currentValue string,
	sourced bool,
) (string, bool, error) {
	return f(ctx, originalKey, resolvedKey, originalValue, currentValue, sourced)
}
