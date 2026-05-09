package env

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- Mutator interface + MutatorFunc ---

func TestMutatorFunc_AdaptsCorrectly(t *testing.T) {
	called := 0
	var fn MutatorFunc = func(_ context.Context, originalKey, resolvedKey, originalValue, currentValue string, sourced bool) (string, bool, error) {
		called++
		if originalKey != "FOO" {
			t.Errorf("originalKey = %q", originalKey)
		}
		if resolvedKey != "APP_FOO" {
			t.Errorf("resolvedKey = %q", resolvedKey)
		}
		if originalValue != "raw" {
			t.Errorf("originalValue = %q", originalValue)
		}
		if currentValue != "raw" {
			t.Errorf("currentValue = %q", currentValue)
		}
		if !sourced {
			t.Errorf("sourced = false")
		}
		return "rewritten", false, nil
	}
	got, stop, err := fn.MutateEnv(context.Background(), "FOO", "APP_FOO", "raw", "raw", true)
	if err != nil || stop || got != "rewritten" {
		t.Errorf("got (%q, %v, %v)", got, stop, err)
	}
	if called != 1 {
		t.Errorf("called %d times, want 1", called)
	}
}

// --- mutator chain in Load ---

func TestMutator_RewritesValue(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO"`
	}
	loader := New(
		WithSource(Map(map[string]string{"FOO": "original"})),
		WithMutator(MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
			return "mutated:" + current, false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Foo != "mutated:original" {
		t.Errorf("Foo = %q", c.Foo)
	}
}

func TestMutator_ChainOrdering(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO"`
	}
	first := MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
		return current + "-first", false, nil
	})
	second := MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
		return current + "-second", false, nil
	})
	loader := New(
		WithSource(Map(map[string]string{"FOO": "x"})),
		WithMutator(first, second),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Foo != "x-first-second" {
		t.Errorf("Foo = %q (chain order broken)", c.Foo)
	}
}

func TestMutator_AppendOrdering(t *testing.T) {
	// WithMutator may be called multiple times; entries accumulate in
	// registration order.
	type cfg struct {
		Foo string `env:"FOO"`
	}
	loader := New(
		WithSource(Map(map[string]string{"FOO": "x"})),
		WithMutator(MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
			return current + "-A", false, nil
		})),
		WithMutator(MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
			return current + "-B", false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Foo != "x-A-B" {
		t.Errorf("Foo = %q", c.Foo)
	}
}

func TestMutator_StopHaltsChain(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO"`
	}
	loader := New(
		WithSource(Map(map[string]string{"FOO": "x"})),
		WithMutator(
			MutatorFunc(func(_ context.Context, _, _, _, _ string, _ bool) (string, bool, error) {
				return "stopped-here", true, nil
			}),
			MutatorFunc(func(_ context.Context, _, _, _, _ string, _ bool) (string, bool, error) {
				return "should-not-run", false, nil
			}),
		),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Foo != "stopped-here" {
		t.Errorf("Foo = %q (stop didn't halt chain)", c.Foo)
	}
}

func TestMutator_ErrorTerminatesField(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO"`
		Bar string `env:"BAR,default=fine"`
	}
	mutErr := errors.New("mutator failed")
	loader := New(
		WithSource(Map(map[string]string{"FOO": "x"})),
		WithMutator(MutatorFunc(func(_ context.Context, _, key, _, _ string, _ bool) (string, bool, error) {
			if key == "FOO" {
				return "", false, mutErr
			}
			return "ok", false, nil
		})),
	)
	var c cfg
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, mutErr) {
		t.Errorf("err does not unwrap to mutator error: %v", err)
	}
	// Bar should still be processed despite Foo's failure. The mutator
	// returns "ok" for Bar, so Bar gets "ok" (not the default "fine"
	// which would only apply if the mutator didn't run).
	if c.Bar != "ok" {
		t.Errorf("Bar = %q (chain should continue after one field fails)", c.Bar)
	}
}

func TestMutator_SynthesizesValueForUnset(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO,required"`
	}
	loader := New(
		WithSource(Map(map[string]string{})), // empty source
		WithMutator(MutatorFunc(func(_ context.Context, _, _, _, _ string, sourced bool) (string, bool, error) {
			if sourced {
				t.Errorf("sourced should be false for unset field")
			}
			return "synthesized", false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatalf("required field should be satisfied by synthesized value: %v", err)
	}
	if c.Foo != "synthesized" {
		t.Errorf("Foo = %q", c.Foo)
	}
}

func TestMutator_OriginalValueIsSourceView(t *testing.T) {
	// Even when a default is applied, originalValue passed to the mutator
	// should be the source's value (or "" when not sourced) — defaults
	// don't appear in originalValue.
	type cfg struct {
		Foo string `env:"FOO,default=fallback"`
	}
	var capturedOriginal string
	var capturedCurrent string
	var capturedSourced bool
	loader := New(
		WithSource(Map(map[string]string{})), // unset
		WithMutator(MutatorFunc(func(_ context.Context, _, _, original, current string, sourced bool) (string, bool, error) {
			capturedOriginal = original
			capturedCurrent = current
			capturedSourced = sourced
			return current, false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if capturedOriginal != "" {
		t.Errorf("originalValue = %q, want empty (source had no value)", capturedOriginal)
	}
	if capturedCurrent != "fallback" {
		t.Errorf("currentValue = %q, want \"fallback\" (default applied)", capturedCurrent)
	}
	if capturedSourced {
		t.Errorf("sourced = true, want false (source didn't have key)")
	}
	if c.Foo != "fallback" {
		t.Errorf("Foo = %q", c.Foo)
	}
}

func TestMutator_ContextPropagation(t *testing.T) {
	type cfg struct {
		Foo string `env:"FOO,default=x"`
	}
	type ctxKey struct{}
	type ctxVal struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, &ctxVal{})

	var captured context.Context
	loader := New(
		WithMutator(MutatorFunc(func(c context.Context, _, _, _, current string, _ bool) (string, bool, error) {
			captured = c //nolint:fatcontext // test capture is intentional
			return current, false, nil
		})),
	)
	var c cfg
	if err := loader.LoadContext(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Value(ctxKey{}) == nil {
		t.Errorf("mutator did not receive the LoadContext's context value")
	}
}

// --- secret:// indirection scenario ---

func TestMutator_SecretIndirection(t *testing.T) {
	// Realistic use: a value of "secret://path/to/key" is rewritten to
	// the actual secret content. Models a vault adapter.
	vault := map[string]string{
		"app/db_password": "super-secret-pw",
	}
	type cfg struct {
		DBPassword string `env:"DB_PASSWORD,required,secret"`
	}
	loader := New(
		WithSource(Map(map[string]string{"DB_PASSWORD": "secret://app/db_password"})),
		WithMutator(MutatorFunc(func(_ context.Context, _, _, _, current string, _ bool) (string, bool, error) {
			if !strings.HasPrefix(current, "secret://") {
				return current, false, nil
			}
			path := strings.TrimPrefix(current, "secret://")
			val, ok := vault[path]
			if !ok {
				return "", false, errors.New("secret not found: " + path)
			}
			return val, false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.DBPassword != "super-secret-pw" {
		t.Errorf("DBPassword = %q", c.DBPassword)
	}
}

// --- mutator does not run for env:"-" fields ---

func TestMutator_SkipsTagDashFields(t *testing.T) {
	type cfg struct {
		Skipped string `env:"-"`
		Active  string `env:"ACTIVE,default=x"`
	}
	called := 0
	loader := New(
		WithMutator(MutatorFunc(func(_ context.Context, _, key, _, _ string, _ bool) (string, bool, error) {
			called++
			if key == "" {
				t.Errorf("mutator invoked with empty key (should skip env:\"-\")")
			}
			return "x", false, nil
		})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	// Only ACTIVE (1 field with a tag) should invoke the mutator.
	if called != 1 {
		t.Errorf("mutator invoked %d times, want 1 (env:\"-\" must be skipped)", called)
	}
}
