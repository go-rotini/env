package env

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOSEnv_Lookup_Set(t *testing.T) {
	t.Setenv("ENVTEST_OSENV_LOOKUP_SET", "value")
	v, ok := OSEnv().Lookup("ENVTEST_OSENV_LOOKUP_SET")
	if !ok {
		t.Fatal("ok=false for set variable")
	}
	if v != "value" {
		t.Errorf("got %q, want %q", v, "value")
	}
}

func TestOSEnv_Lookup_Empty(t *testing.T) {
	t.Setenv("ENVTEST_OSENV_LOOKUP_EMPTY", "")
	v, ok := OSEnv().Lookup("ENVTEST_OSENV_LOOKUP_EMPTY")
	if !ok {
		t.Fatal("set-but-empty variable reported as unset")
	}
	if v != "" {
		t.Errorf("got %q, want empty string", v)
	}
}

func TestOSEnv_Lookup_Unset(t *testing.T) {
	const key = "ENVTEST_OSENV_LOOKUP_UNSET_DEFINITELY_NOT_SET"
	v, ok := OSEnv().Lookup(key)
	if ok {
		t.Errorf("ok=true for unset variable; got value %q", v)
	}
	if v != "" {
		t.Errorf("non-empty value for unset variable: %q", v)
	}
}

func TestOSEnv_Lookup_DegenerateKeys(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"leading_equals", "=C:"},
		{"contains_equals", "FOO=BAR"},
		{"contains_nul", "FOO\x00BAR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := OSEnv().Lookup(tc.key)
			if ok {
				t.Errorf("ok=true for degenerate key %q (got %q)", tc.key, v)
			}
			if v != "" {
				t.Errorf("non-empty value for degenerate key %q: %q", tc.key, v)
			}
		})
	}
}

func TestOSEnv_Singleton(t *testing.T) {
	a := OSEnv()
	b := OSEnv()
	if a != b {
		t.Errorf("OSEnv() returns different instances: %p vs %p", a, b)
	}
}

func TestOSEnv_PlatformCase(t *testing.T) {
	t.Setenv("ENVTEST_OSENV_PLATFORM_CASE", "value")
	_, okExact := OSEnv().Lookup("ENVTEST_OSENV_PLATFORM_CASE")
	if !okExact {
		t.Fatal("exact-case lookup failed")
	}

	_, okFolded := OSEnv().Lookup("envtest_osenv_platform_case")
	switch runtime.GOOS {
	case "windows":
		if !okFolded {
			t.Error("Windows OSEnv should be case-insensitive but folded lookup returned ok=false")
		}
	default:
		if okFolded {
			t.Error("POSIX OSEnv should be case-sensitive but folded lookup returned ok=true")
		}
	}
}

func TestMap_Basic(t *testing.T) {
	src := Map(map[string]string{"FOO": "bar", "BAZ": ""})

	v, ok := src.Lookup("FOO")
	if !ok || v != "bar" {
		t.Errorf("FOO lookup: got (%q, %v), want (\"bar\", true)", v, ok)
	}

	v, ok = src.Lookup("BAZ")
	if !ok || v != "" {
		t.Errorf("BAZ (empty) lookup: got (%q, %v), want (\"\", true)", v, ok)
	}

	v, ok = src.Lookup("MISSING")
	if ok || v != "" {
		t.Errorf("MISSING lookup: got (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestMap_NilInput(t *testing.T) {
	src := Map(nil)
	v, ok := src.Lookup("FOO")
	if ok || v != "" {
		t.Errorf("nil-map lookup: got (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestMap_Empty(t *testing.T) {
	src := Map(map[string]string{})
	v, ok := src.Lookup("FOO")
	if ok || v != "" {
		t.Errorf("empty-map lookup: got (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestMap_CopyAtConstruction(t *testing.T) {
	original := map[string]string{"FOO": "bar"}
	src := Map(original)

	original["FOO"] = "mutated"
	original["NEW"] = "extra"
	delete(original, "FOO")

	v, ok := src.Lookup("FOO")
	if !ok || v != "bar" {
		t.Errorf("FOO after mutation: got (%q, %v), want (\"bar\", true)", v, ok)
	}
	if _, ok := src.Lookup("NEW"); ok {
		t.Error("Map source picked up post-construction insertion")
	}
}

func TestMap_CaseInsensitive(t *testing.T) {
	src := Map(
		map[string]string{"Path": "/usr/bin", "DEBUG": "1"},
		WithCaseInsensitive(),
	)

	cases := []string{"Path", "PATH", "path", "PaTh"}
	for _, key := range cases {
		v, ok := src.Lookup(key)
		if !ok || v != "/usr/bin" {
			t.Errorf("Lookup(%q): got (%q, %v), want (\"/usr/bin\", true)", key, v, ok)
		}
	}

	v, ok := src.Lookup("debug")
	if !ok || v != "1" {
		t.Errorf("debug (case-insensitive): got (%q, %v), want (\"1\", true)", v, ok)
	}

	if _, ok := src.Lookup("MISSING"); ok {
		t.Error("MISSING should not match anything in case-insensitive mode")
	}
}

func TestMap_CaseSensitiveByDefault(t *testing.T) {
	src := Map(map[string]string{"Path": "/usr/bin"})
	if _, ok := src.Lookup("PATH"); ok {
		t.Error("default Map should be case-sensitive; PATH matched Path")
	}
	if _, ok := src.Lookup("path"); ok {
		t.Error("default Map should be case-sensitive; path matched Path")
	}
}

func TestMulti_FirstWins(t *testing.T) {
	a := Map(map[string]string{"FOO": "from_a"})
	b := Map(map[string]string{"FOO": "from_b"})
	src := Multi(a, b)

	v, ok := src.Lookup("FOO")
	if !ok || v != "from_a" {
		t.Errorf("got (%q, %v), want (\"from_a\", true)", v, ok)
	}
}

func TestMulti_Fallthrough(t *testing.T) {
	a := Map(map[string]string{"OTHER": "x"})
	b := Map(map[string]string{"FOO": "from_b"})
	src := Multi(a, b)

	v, ok := src.Lookup("FOO")
	if !ok || v != "from_b" {
		t.Errorf("got (%q, %v), want (\"from_b\", true)", v, ok)
	}
}

func TestMulti_AllMiss(t *testing.T) {
	a := Map(map[string]string{"X": "1"})
	b := Map(map[string]string{"Y": "2"})
	src := Multi(a, b)

	v, ok := src.Lookup("Z")
	if ok || v != "" {
		t.Errorf("got (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestMulti_Empty(t *testing.T) {
	src := Multi()
	v, ok := src.Lookup("FOO")
	if ok || v != "" {
		t.Errorf("got (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestMulti_Single(t *testing.T) {
	inner := Map(map[string]string{"FOO": "bar"})
	src := Multi(inner)

	if src != inner {
		t.Error("Multi(s) should return s without indirection")
	}
}

func TestMulti_RespectsEmptyOk(t *testing.T) {
	// Source A has FOO set to empty (ok=true). Source B has FOO=value.
	// First-match-wins must return A's empty value, not fall through to B.
	a := Map(map[string]string{"FOO": ""})
	b := Map(map[string]string{"FOO": "from_b"})
	src := Multi(a, b)

	v, ok := src.Lookup("FOO")
	if !ok {
		t.Fatal("Multi should have returned A's set-but-empty value, got ok=false")
	}
	if v != "" {
		t.Errorf("Multi fell through past set-but-empty value; got %q", v)
	}
}

func TestSourceConcurrentLookup(t *testing.T) {
	t.Setenv("ENVTEST_CONCURRENT_OS", "os_value")

	mapSrc := Map(map[string]string{
		"A": "1", "B": "2", "C": "3", "D": "4", "E": "5",
	})
	multiSrc := Multi(
		Map(map[string]string{"X": "first"}),
		Map(map[string]string{"X": "second", "Y": "fallback"}),
	)
	insensitive := Map(
		map[string]string{"Mixed": "case"},
		WithCaseInsensitive(),
	)

	const goroutines = 32
	const iterations = 1000

	var wg sync.WaitGroup
	var bad atomic.Int64

	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				if v, ok := OSEnv().Lookup("ENVTEST_CONCURRENT_OS"); !ok || v != "os_value" {
					bad.Add(1)
				}
				if v, ok := mapSrc.Lookup("C"); !ok || v != "3" {
					bad.Add(1)
				}
				if v, ok := multiSrc.Lookup("X"); !ok || v != "first" {
					bad.Add(1)
				}
				if v, ok := multiSrc.Lookup("Y"); !ok || v != "fallback" {
					bad.Add(1)
				}
				if v, ok := insensitive.Lookup("MIXED"); !ok || v != "case" {
					bad.Add(1)
				}
				_ = g
			}
		}()
	}
	wg.Wait()

	if got := bad.Load(); got != 0 {
		t.Errorf("concurrent lookups produced %d incorrect results", got)
	}
}

func TestAsciiUpper(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"FOO", "FOO"},
		{"foo", "FOO"},
		{"FoO", "FOO"},
		{"foo123_bar", "FOO123_BAR"},
		{"caf\xc3\xa9", "CAF\xc3\xa9"}, // non-ASCII bytes preserved verbatim
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := asciiUpper(tc.in); got != tc.want {
				t.Errorf("asciiUpper(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAsciiUpper_NoFoldShortCircuit(t *testing.T) {
	// All-uppercase or non-letter input should return the original string
	// (the short-circuit branch). Verify the result is byte-identical.
	in := "ALREADY_UPPER_123"
	got := asciiUpper(in)
	if got != in {
		t.Errorf("asciiUpper(%q) = %q, want unchanged", in, got)
	}
}
