package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- happy path ---

type loaderBasicConfig struct {
	Port    int           `env:"PORT,default=8080"`
	DBURL   string        `env:"DATABASE_URL,required"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"`
	Debug   bool          // DEBUG via name transform
}

func TestLoader_Load_HappyPath(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"DATABASE_URL": "postgres://localhost",
		"DEBUG":        "true",
	})))
	var cfg loaderBasicConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d (default not applied)", cfg.Port)
	}
	if cfg.DBURL != "postgres://localhost" {
		t.Errorf("DBURL = %q", cfg.DBURL)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v (default not applied)", cfg.Timeout)
	}
	if !cfg.Debug {
		t.Errorf("Debug not set")
	}
}

func TestLoader_Load_RequiredMissing(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{})))
	var cfg loaderBasicConfig
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !errors.Is(err, ErrMissingRequired) {
		t.Errorf("err = %v, want ErrMissingRequired", err)
	}
	if !errors.Is(err, ErrMultiError) {
		t.Errorf("err should be MultiError")
	}
}

// --- multi-error aggregation ---

type loaderMultiErrConfig struct {
	A string `env:"A,required"`
	B int    `env:"B,required"`
	C string `env:"C,notEmpty"`
}

func TestLoader_Load_AggregatesMultipleErrors(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"B": "not-an-int",
		"C": "",
	})))
	var cfg loaderMultiErrConfig
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	var multi *MultiError
	if !errors.As(err, &multi) {
		t.Fatalf("err is not *MultiError: %T", err)
	}
	if len(multi.Errors) != 3 {
		t.Errorf("got %d errors, want 3 (missing A + parse B + empty C):\n%s", len(multi.Errors), err)
	}
	// Each should be reachable.
	if !errors.Is(err, ErrMissingRequired) {
		t.Errorf("missing-required not in chain")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("parse-error not in chain")
	}
	if !errors.Is(err, ErrEmptyValue) {
		t.Errorf("empty-value not in chain")
	}
}

// --- ParseError annotation ---

type loaderParseErrConfig struct {
	Port int `env:"PORT,required"`
}

func TestLoader_Load_ParseErrorCarriesFieldContext(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"PORT": "abc"})))
	var cfg loaderParseErrConfig
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("no *ParseError in chain: %v", err)
	}
	if pe.Field != "Port" {
		t.Errorf("Field = %q, want \"Port\"", pe.Field)
	}
	if pe.Key != "PORT" {
		t.Errorf("Key = %q, want \"PORT\"", pe.Key)
	}
	if pe.Value != "abc" {
		t.Errorf("Value = %q", pe.Value)
	}
	if !strings.Contains(pe.Type, "int") {
		t.Errorf("Type = %q", pe.Type)
	}
}

// --- nested struct ---

type loaderHTTPConfig struct {
	Port    int           `env:"PORT,default=8080"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"`
}

type loaderDBConfig struct {
	URL  string `env:"URL,required"`
	Pool int    `env:"POOL,default=5"`
}

type loaderNestedConfig struct {
	HTTP loaderHTTPConfig `envPrefix:"HTTP_"`
	DB   loaderDBConfig   `envPrefix:"DB_"`
}

func TestLoader_Load_Nested(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"HTTP_PORT": "9000",
		"DB_URL":    "postgres://db",
	})))
	var cfg loaderNestedConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.HTTP.Port != 9000 {
		t.Errorf("HTTP.Port = %d", cfg.HTTP.Port)
	}
	if cfg.HTTP.Timeout != 30*time.Second {
		t.Errorf("HTTP.Timeout default not applied")
	}
	if cfg.DB.URL != "postgres://db" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.DB.Pool != 5 {
		t.Errorf("DB.Pool default not applied")
	}
}

// --- loader-level prefix ---

func TestLoader_WithPrefix(t *testing.T) {
	loader := New(
		WithPrefix("APP_"),
		WithSource(Map(map[string]string{
			"APP_HTTP_PORT": "9000",
			"APP_DB_URL":    "postgres://db",
		})),
	)
	var cfg loaderNestedConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.HTTP.Port != 9000 || cfg.DB.URL != "postgres://db" {
		t.Errorf("prefix not applied: %+v", cfg)
	}
}

// --- pointer-to-struct nested ---

type loaderPointerNested struct {
	HTTP *loaderHTTPConfig `envPrefix:"HTTP_"`
}

func TestLoader_Load_PointerNested(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"HTTP_PORT": "9000",
	})))
	var cfg loaderPointerNested
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.HTTP == nil {
		t.Fatal("HTTP pointer not auto-allocated")
	}
	if cfg.HTTP.Port != 9000 {
		t.Errorf("HTTP.Port = %d", cfg.HTTP.Port)
	}
}

// --- v validation ---

func TestLoader_Load_NilTarget(t *testing.T) {
	loader := New()
	if err := loader.Load(nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestLoader_Load_NonPointer(t *testing.T) {
	loader := New()
	var cfg loaderBasicConfig
	if err := loader.Load(cfg); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestLoader_Load_NilPointer(t *testing.T) {
	loader := New()
	var cfg *loaderBasicConfig
	if err := loader.Load(cfg); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestLoader_Load_NotStruct(t *testing.T) {
	loader := New()
	var x int
	if err := loader.Load(&x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

// --- structInfo error propagation ---

type loaderConflictConfig struct {
	A string `env:"FOO"`
	B string `env:"FOO"`
}

func TestLoader_Load_StructInfoError(t *testing.T) {
	loader := New()
	var cfg loaderConflictConfig
	err := loader.Load(&cfg)
	if !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", err)
	}
	// Should NOT be wrapped in MultiError (collection errors are permanent).
	if errors.Is(err, ErrMultiError) {
		t.Errorf("collection error wrapped in MultiError; expected direct return")
	}
}

// --- empty / set distinction ---

type loaderEmptyTagConfig struct {
	Foo string `env:"FOO"`
	Bar *int   `env:"BAR"`
}

func TestLoader_Load_EmptyValue(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"FOO": "",
		"BAR": "",
	})))
	var cfg loaderEmptyTagConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Foo != "" {
		t.Errorf("Foo = %q, want empty", cfg.Foo)
	}
	// *int with empty source should auto-allocate to 0.
	if cfg.Bar == nil {
		t.Error("Bar = nil; want non-nil pointer to zero")
	} else if *cfg.Bar != 0 {
		t.Errorf("*Bar = %d, want 0", *cfg.Bar)
	}
}

func TestLoader_Load_UnsetOptional(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{})))
	var cfg loaderEmptyTagConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Foo != "" {
		t.Errorf("Foo = %q, want zero (unset, not required)", cfg.Foo)
	}
	if cfg.Bar != nil {
		t.Errorf("Bar = %v, want nil (unset)", cfg.Bar)
	}
}

// --- WithRequireAll ---

type loaderRequireAllConfig struct {
	A string `env:"A"`           // would be optional by default
	B string `env:"B,default=x"` // has default → not required even with WithRequireAll
}

func TestLoader_WithRequireAll(t *testing.T) {
	loader := New(
		WithRequireAll(),
		WithSource(Map(map[string]string{})),
	)
	var cfg loaderRequireAllConfig
	err := loader.Load(&cfg)
	if !errors.Is(err, ErrMissingRequired) {
		t.Errorf("err = %v, want ErrMissingRequired", err)
	}
	// B should not be flagged (has default).
	var multi *MultiError
	errors.As(err, &multi)
	if multi != nil && len(multi.Errors) != 1 {
		t.Errorf("got %d errors, want 1 (only A; B has default):\n%s", len(multi.Errors), err)
	}
}

func TestLoader_WithRequireAll_Satisfied(t *testing.T) {
	loader := New(
		WithRequireAll(),
		WithSource(Map(map[string]string{"A": "value"})),
	)
	var cfg loaderRequireAllConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.A != "value" || cfg.B != "x" {
		t.Errorf("got %+v", cfg)
	}
}

// --- WithTagName / WithFieldNameTransform (cache bypass) ---

type loaderCustomTagConfig struct {
	Port int `envconfig:"PORT,default=8080"`
}

func TestLoader_WithTagName(t *testing.T) {
	loader := New(
		WithTagName("envconfig"),
		WithSource(Map(map[string]string{"PORT": "9000"})),
	)
	var cfg loaderCustomTagConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Port != 9000 {
		t.Errorf("Port = %d", cfg.Port)
	}
}

func TestLoader_WithFieldNameTransform(t *testing.T) {
	type cfg struct {
		Port int
	}
	loader := New(
		WithFieldNameTransform(func(s string) string { return "PFX_" + strings.ToUpper(s) }),
		WithSource(Map(map[string]string{"PFX_PORT": "9000"})),
	)
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.Port != 9000 {
		t.Errorf("Port = %d", c.Port)
	}
}

// --- LoadContext ---

func TestLoader_LoadContext(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"DATABASE_URL": "postgres://localhost",
	})))
	var cfg loaderBasicConfig
	if err := loader.LoadContext(context.Background(), &cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.DBURL != "postgres://localhost" {
		t.Errorf("got %q", cfg.DBURL)
	}
}

// --- Snapshot ---

func TestLoader_Snapshot(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{
		"DATABASE_URL": "postgres://live",
		"DEBUG":        "true",
	})))
	var cfg loaderBasicConfig
	got, err := loader.Snapshot(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got["DATABASE_URL"] != "postgres://live" {
		t.Errorf("DATABASE_URL = %q", got["DATABASE_URL"])
	}
	// Defaults filled in for unset keys.
	if got["PORT"] != "8080" {
		t.Errorf("PORT = %q (default not snapped)", got["PORT"])
	}
	if got["TIMEOUT"] != "30s" {
		t.Errorf("TIMEOUT = %q (default not snapped)", got["TIMEOUT"])
	}
	// Snapshot a key that has neither value nor default → omitted.
	type cfgNoDefault struct {
		X string `env:"X"`
	}
	loader2 := New(WithSource(Map(map[string]string{})))
	var c2 cfgNoDefault
	snap, err := loader2.Snapshot(&c2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["X"]; ok {
		t.Errorf("X should be omitted when unset and no default; got %q", snap["X"])
	}
}

func TestLoader_Snapshot_AcceptsValueOrPointer(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"PORT": "9999"})))
	var cfg loaderBasicConfig
	if _, err := loader.Snapshot(&cfg); err != nil {
		t.Errorf("pointer form err: %v", err)
	}
	if _, err := loader.Snapshot(cfg); err != nil {
		t.Errorf("value form err: %v", err)
	}
}

func TestLoader_Snapshot_NilPointer(t *testing.T) {
	loader := New()
	if _, err := loader.Snapshot(nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
	var cfg *loaderBasicConfig
	if _, err := loader.Snapshot(cfg); !errors.Is(err, ErrNilPointer) {
		t.Errorf("nil ptr: err = %v, want ErrNilPointer", err)
	}
}

func TestLoader_Snapshot_NotStruct(t *testing.T) {
	loader := New()
	var x int
	if _, err := loader.Snapshot(&x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

func TestLoader_Snapshot_StructInfoError(t *testing.T) {
	loader := New()
	var cfg loaderConflictConfig
	if _, err := loader.Snapshot(&cfg); !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v", err)
	}
}

// --- WithCustomDecoder integration ---

type myStringID string

type cfgWithCustomDec struct {
	ID myStringID `env:"ID"`
}

func TestLoader_WithCustomDecoder(t *testing.T) {
	loader := New(
		WithSource(Map(map[string]string{"ID": "abc"})),
		WithCustomDecoder(func(s string, dst *myStringID) error {
			*dst = myStringID("ID-" + s)
			return nil
		}),
	)
	var cfg cfgWithCustomDec
	if err := loader.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "ID-abc" {
		t.Errorf("ID = %q, want ID-abc", cfg.ID)
	}
}

func TestLoader_WithCustomDecoder_Error(t *testing.T) {
	loader := New(
		WithSource(Map(map[string]string{"ID": "x"})),
		WithCustomDecoder(func(string, *myStringID) error {
			return errors.New("custom decode failed")
		}),
	)
	var cfg cfgWithCustomDec
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "custom decode failed") {
		t.Errorf("err = %v", err)
	}
}

// --- top-level entry points ---

func TestLoad_Default(t *testing.T) {
	t.Setenv("ENVTEST_LOAD_DEFAULT", "value")
	type c struct {
		Field string `env:"ENVTEST_LOAD_DEFAULT"`
	}
	var cfg c
	if err := Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Field != "value" {
		t.Errorf("Field = %q", cfg.Field)
	}
}

func TestLoad_NilDelegatesError(t *testing.T) {
	if err := Load(nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v", err)
	}
}

func TestMustLoad_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustLoad did not panic on missing required field")
		}
	}()
	t.Setenv("ENVTEST_MUSTLOAD_REQ", "")
	type c struct {
		X string `env:"ENVTEST_MUSTLOAD_THIS_KEY_IS_NEVER_SET,required"`
	}
	var cfg c
	MustLoad(&cfg)
}

func TestMustLoad_Success(t *testing.T) {
	t.Setenv("ENVTEST_MUSTLOAD_OK", "x")
	type c struct {
		X string `env:"ENVTEST_MUSTLOAD_OK"`
	}
	var cfg c
	MustLoad(&cfg) // must not panic
	if cfg.X != "x" {
		t.Errorf("got %q", cfg.X)
	}
}

func TestLoadTo(t *testing.T) {
	type c struct {
		X int `env:"ENVTEST_LOADTO_X,default=42"`
	}
	got, err := LoadTo[c]()
	if err != nil {
		t.Fatal(err)
	}
	if got.X != 42 {
		t.Errorf("X = %d", got.X)
	}
}

func TestLoadTo_WithOptions(t *testing.T) {
	type c struct {
		X int `env:"X"`
	}
	got, err := LoadTo[c](WithSource(Map(map[string]string{"X": "9000"})))
	if err != nil {
		t.Fatal(err)
	}
	if got.X != 9000 {
		t.Errorf("X = %d", got.X)
	}
}

func TestLoadTo_Error(t *testing.T) {
	type c struct {
		X string `env:"ENVTEST_LOADTO_REQ_NEVER_SET,required"`
	}
	got, err := LoadTo[c]()
	if err == nil {
		t.Fatal("expected error")
	}
	// On error we get the zero value back.
	if got != (c{}) {
		t.Errorf("got non-zero value on error: %+v", got)
	}
}

func TestMustLoadTo(t *testing.T) {
	type c struct {
		X int `env:"X"`
	}
	got := MustLoadTo[c](WithSource(Map(map[string]string{"X": "9000"})))
	if got.X != 9000 {
		t.Errorf("got %+v", got)
	}
}

func TestMustLoadTo_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustLoadTo did not panic")
		}
	}()
	type c struct {
		X string `env:"ENVTEST_MUSTLOADTO_REQ_NEVER_SET,required"`
	}
	_ = MustLoadTo[c]()
}

// --- empty-source loader ---

func TestLoader_EmptySourceChain(t *testing.T) {
	loader := New(WithSource())
	type c struct {
		X string `env:"X,default=fallback"`
	}
	var cfg c
	if err := loader.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.X != "fallback" {
		t.Errorf("default not applied with empty source chain: %q", cfg.X)
	}
}

// --- fromFile tag ---

func TestLoader_FromFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("from-file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type cfg struct {
		Token string `env:"TOKEN_FILE,fromFile"`
	}
	loader := New(WithSource(Map(map[string]string{"TOKEN_FILE": path})))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	// Trailing \n is stripped.
	if c.Token != "from-file-value" {
		t.Errorf("Token = %q", c.Token)
	}
}

func TestLoader_FromFile_TrimsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("value\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type cfg struct {
		Token string `env:"TOKEN_FILE,fromFile"`
	}
	loader := New(WithSource(Map(map[string]string{"TOKEN_FILE": path})))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Token != "value" {
		t.Errorf("Token = %q (CRLF not stripped)", c.Token)
	}
}

func TestLoader_FromFile_NotFound(t *testing.T) {
	type cfg struct {
		Token string `env:"TOKEN_FILE,fromFile"`
	}
	loader := New(WithSource(Map(map[string]string{"TOKEN_FILE": "/no/such/file.txt"})))
	var c cfg
	err := loader.Load(&c)
	if !errors.Is(err, ErrFromFileRead) {
		t.Errorf("err = %v, want ErrFromFileRead", err)
	}
}

func TestLoader_FromFile_EmptyValueIsSkipped(t *testing.T) {
	// fromFile only fires on non-empty values; an empty value passes
	// through unchanged.
	type cfg struct {
		Token string `env:"TOKEN_FILE,fromFile"`
	}
	loader := New(WithSource(Map(map[string]string{"TOKEN_FILE": ""})))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Token != "" {
		t.Errorf("Token = %q, want empty", c.Token)
	}
}

// --- unset tag ---

func TestLoader_UnsetTag(t *testing.T) {
	const key = "ENVTEST_UNSET_AFTER_LOAD"
	t.Setenv(key, "secret-value")
	type cfg struct {
		Field string `env:"ENVTEST_UNSET_AFTER_LOAD,unset"`
	}
	var c cfg
	if err := Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Field != "secret-value" {
		t.Errorf("Field = %q (decode should still see the value)", c.Field)
	}
	// After Load, the env var should be unset.
	if _, ok := os.LookupEnv(key); ok {
		t.Errorf("env var still set after Load with unset tag")
	}
}

func TestLoader_WithoutUnset(t *testing.T) {
	const key = "ENVTEST_WITHOUT_UNSET"
	t.Setenv(key, "still-here")
	type cfg struct {
		Field string `env:"ENVTEST_WITHOUT_UNSET,unset"`
	}
	loader := New(WithoutUnset())
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv(key); !ok {
		t.Errorf("env var was unset despite WithoutUnset")
	}
}

// --- single-source path (composedSource shortcut) ---

func TestLoader_SingleSource(t *testing.T) {
	src := Map(map[string]string{"X": "1"})
	loader := New(WithSource(src))
	type c struct {
		X int `env:"X"`
	}
	var cfg c
	if err := loader.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.X != 1 {
		t.Errorf("X = %d", cfg.X)
	}
}

// --- multi-source path (composedSource Multi(...) branch) ---

func TestLoader_MultiSource_Precedence(t *testing.T) {
	high := Map(map[string]string{"X": "from_high"})
	low := Map(map[string]string{"X": "from_low", "Y": "only_low"})
	loader := New(WithSource(high, low))
	type c struct {
		X string `env:"X"`
		Y string `env:"Y"`
	}
	var cfg c
	if err := loader.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.X != "from_high" {
		t.Errorf("X = %q, want from_high (first source wins)", cfg.X)
	}
	if cfg.Y != "only_low" {
		t.Errorf("Y = %q, want only_low (fallthrough)", cfg.Y)
	}
}

// --- collectStructInfoFresh non-struct path (custom-config loader) ---

func TestLoader_NonStruct_CustomConfig(t *testing.T) {
	loader := New(WithTagName("envconfig")) // custom config bypasses cache
	var x int
	if err := loader.Load(&x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

// LoadContext intercepts non-struct targets before reaching
// collectStructInfoFresh, so a direct unit-test of the internal method
// is needed to exercise the non-struct guard.
func TestLoader_collectStructInfoFresh_NonStruct(t *testing.T) {
	loader := New(WithTagName("custom"))
	info := loader.collectStructInfoFresh(reflect.TypeFor[int]())
	if !errors.Is(info.Err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", info.Err)
	}
}

// --- annotateParseError filling in empty Type/Value ---

type customDecodeReturnsBareErr struct{}

type cfgWithBarePEDecoder struct {
	X customDecodeReturnsBareErr `env:"X"`
}

func TestLoader_AnnotateParseError_FillsEmptyFields(t *testing.T) {
	// A custom decoder that returns a *ParseError with no Type/Value
	// fields set. The loader should fill them in from field context.
	loader := New(
		WithSource(Map(map[string]string{"X": "the-value"})),
		WithCustomDecoder(func(_ string, _ *customDecodeReturnsBareErr) error {
			return &ParseError{Cause: errors.New("inner")}
		}),
	)
	var cfg cfgWithBarePEDecoder
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError in chain, got %T: %v", err, err)
	}
	if pe.Type == "" {
		t.Errorf("Type was not filled in from field context")
	}
	if pe.Value != "the-value" {
		t.Errorf("Value = %q, want \"the-value\"", pe.Value)
	}
	if pe.Field != "X" {
		t.Errorf("Field = %q, want \"X\"", pe.Field)
	}
}

func TestLoader_AnnotateParseError_LeavesNonParseErrorUnchanged(t *testing.T) {
	// A custom decoder that returns a NON-ParseError. annotateParseError
	// should pass it through unchanged (not wrapped in a new ParseError).
	loader := New(
		WithSource(Map(map[string]string{"X": "x"})),
		WithCustomDecoder(func(_ string, _ *customDecodeReturnsBareErr) error {
			return errors.New("plain error")
		}),
	)
	var cfg cfgWithBarePEDecoder
	err := loader.Load(&cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "plain error") {
		t.Errorf("plain error message lost: %v", err)
	}
	var pe *ParseError
	if errors.As(err, &pe) {
		t.Errorf("plain error was wrapped in *ParseError unexpectedly")
	}
}
