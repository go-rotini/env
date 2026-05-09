package env

import "context"

// Mutator observes or rewrites resolved values before they reach the
// decoder. The chain runs once per field per Load, after source lookup
// and default application, before decoding. Common uses: resolving
// indirections (e.g., "secret://path" → vault fetch), synthesizing
// values for unset fields, observation for logging or metrics.
//
// Mutators do NOT run for fields tagged env:"-". They DO run for
// unset fields with no default — a mutator may synthesize a value.
type Mutator interface {
	// MutateEnv inspects or rewrites the value the loader is about to
	// decode.
	//
	//   originalValue : the source's view (no defaults applied);
	//                   "" when sourced is false.
	//   currentValue  : the value after default application and any
	//                   earlier mutators in the chain.
	//   sourced       : false when no Source returned ok=true.
	//
	// stop=true halts the chain immediately and feeds newValue to the
	// decoder. A non-nil err terminates the per-field pipeline.
	MutateEnv(
		ctx context.Context,
		originalKey, resolvedKey, originalValue, currentValue string,
		sourced bool,
	) (newValue string, stop bool, err error)
}

// MutatorFunc adapts a function into a [Mutator].
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
