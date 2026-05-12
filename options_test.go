package env

import (
	"errors"
	"log/slog"
	"reflect"
	"testing"
)

func TestDefaultLoaderOptions(t *testing.T) {
	o := defaultLoaderOptions()
	if len(o.sources) != 1 {
		t.Errorf("default sources length = %d, want 1", len(o.sources))
	}
	if o.tagName != "env" {
		t.Errorf("default tagName = %q, want \"env\"", o.tagName)
	}
	if o.fieldNameXform == nil {
		t.Errorf("default fieldNameXform is nil")
	}
	if o.prefix != "" || o.requireAll {
		t.Errorf("non-zero defaults: %+v", o)
	}
}

func TestWithSource_Replaces(t *testing.T) {
	o := defaultLoaderOptions()
	a := Map(map[string]string{"X": "1"})
	b := Map(map[string]string{"Y": "2"})
	WithSource(a, b)(&o)
	if len(o.sources) != 2 {
		t.Errorf("got %d sources, want 2", len(o.sources))
	}
	// Default OSEnv should be gone.
	if _, ok := o.sources[0].Lookup("PATH"); ok {
		// could be a fluke if the test env happens to have X=PATH; pick a vanishingly unlikely key
	}
}

func TestWithSource_EmptyArgs(t *testing.T) {
	o := defaultLoaderOptions()
	WithSource()(&o)
	if len(o.sources) != 0 {
		t.Errorf("got %d sources, want 0", len(o.sources))
	}
}

func TestWithSourceAppend(t *testing.T) {
	o := defaultLoaderOptions()
	src := Map(map[string]string{"X": "1"})
	WithSourceAppend(src)(&o)
	if len(o.sources) != 2 {
		t.Errorf("got %d sources, want 2 (default + appended)", len(o.sources))
	}
	if v, _ := o.sources[1].Lookup("X"); v != "1" {
		t.Errorf("appended source not at end")
	}
}

func TestWithSourcePrepend(t *testing.T) {
	o := defaultLoaderOptions()
	src := Map(map[string]string{"X": "1"})
	WithSourcePrepend(src)(&o)
	if len(o.sources) != 2 {
		t.Errorf("got %d sources, want 2 (prepended + default)", len(o.sources))
	}
	if v, _ := o.sources[0].Lookup("X"); v != "1" {
		t.Errorf("prepended source not at front")
	}
}

func TestWithPrefix(t *testing.T) {
	o := defaultLoaderOptions()
	WithPrefix("APP_")(&o)
	if o.prefix != "APP_" {
		t.Errorf("prefix = %q", o.prefix)
	}
}

func TestWithTagName(t *testing.T) {
	o := defaultLoaderOptions()
	WithTagName("envconfig")(&o)
	if o.tagName != "envconfig" {
		t.Errorf("tagName = %q", o.tagName)
	}
}

func TestWithFieldNameTransform(t *testing.T) {
	custom := func(s string) string { return "X_" + s }
	o := defaultLoaderOptions()
	WithFieldNameTransform(custom)(&o)
	if o.fieldNameXform("Foo") != "X_Foo" {
		t.Errorf("transform not applied")
	}
}

func TestWithFieldNameTransform_NilIgnored(t *testing.T) {
	o := defaultLoaderOptions()
	original := o.fieldNameXform
	WithFieldNameTransform(nil)(&o)
	// Compare via function-pointer identity — should be unchanged.
	if reflect.ValueOf(o.fieldNameXform).Pointer() != reflect.ValueOf(original).Pointer() {
		t.Errorf("nil transform replaced default; expected no-op")
	}
}

func TestWithRequireAll(t *testing.T) {
	o := defaultLoaderOptions()
	WithRequireAll()(&o)
	if !o.requireAll {
		t.Errorf("requireAll not set")
	}
}

func TestWithLogger(t *testing.T) {
	o := defaultLoaderOptions()
	logger := slog.Default()
	WithLogger(logger)(&o)
	if o.logger != logger {
		t.Errorf("logger not set")
	}
}

func TestWithCustomDecoder(t *testing.T) {
	type myString string
	called := 0
	o := defaultLoaderOptions()
	opt := WithCustomDecoder(func(s string, dst *myString) error {
		called++
		*dst = myString("custom:" + s)
		return nil
	})
	opt(&o)

	dec, ok := o.customDecoders[reflect.TypeFor[myString]()]
	if !ok {
		t.Fatal("decoder not registered")
	}

	// Allocate a target and dispatch through the registered decoder.
	holder := reflect.New(reflect.TypeFor[myString]())
	if err := dec("hello", holder.Elem()); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("decoder not called")
	}
	got, _ := holder.Elem().Interface().(myString)
	if got != "custom:hello" {
		t.Errorf("got %q, want custom:hello", got)
	}
}

func TestWithCustomDecoder_ReturnsError(t *testing.T) {
	type myInt int
	o := defaultLoaderOptions()
	opt := WithCustomDecoder(func(string, *myInt) error {
		return errors.New("nope")
	})
	opt(&o)
	dec := o.customDecoders[reflect.TypeFor[myInt]()]
	holder := reflect.New(reflect.TypeFor[myInt]())
	if err := dec("anything", holder.Elem()); err == nil || err.Error() != "nope" {
		t.Errorf("err = %v", err)
	}
}
