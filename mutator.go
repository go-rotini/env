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
	//   originalKey   : the field's variable name without [WithPrefix].
	//   resolvedKey   : the name looked up in the sources (prefix applied).
	//   originalValue : the source's view (no defaults applied);
	//                   "" when sourced is false.
	//   currentValue  : the value after default application and any
	//                   earlier mutators in the chain.
	//   sourced       : false when no Source returned ok=true.
	//
	// stop=true skips the remaining mutators; newValue continues
	// through the rest of the field pipeline (required check, expand,
	// notEmpty, fromFile, decode). A non-empty newValue counts as a
	// value for an unset field. A non-nil err fails the field.
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
