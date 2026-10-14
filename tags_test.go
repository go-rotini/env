package env

import (
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestToScreamingSnake(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"P", "P"},
		{"Port", "PORT"},
		{"port", "PORT"},
		{"PORT", "PORT"},
		{"DatabaseURL", "DATABASE_URL"},
		{"databaseURL", "DATABASE_URL"},
		{"camelCase", "CAMEL_CASE"},
		{"FooBar", "FOO_BAR"},
		{"FooBarBaz", "FOO_BAR_BAZ"},
		{"HTTPSPort", "HTTPS_PORT"},
		{"HTTPSPortNumber", "HTTPS_PORT_NUMBER"},
		{"IPv4Address", "IPV4_ADDRESS"},
		{"IPv6Addr", "IPV6_ADDR"},
		{"URLs", "URLS"},
		{"URLsAndStuff", "URLS_AND_STUFF"},
		{"USA", "USA"},
		{"already_snake", "ALREADY_SNAKE"},
		{"My_Field", "MY_FIELD"},
		{"MyField_v2", "MY_FIELD_V2"},
		{"X", "X"},
		{"x", "X"},

		// Mixed-case acronym names (OAuth, ECMAScript, etc.) cannot be
		// disambiguated without a dictionary. The algorithm treats the
		// "uppers + 2 lowers" pattern as an acronym-end boundary, which
		// yields "O_AUTH2_TOKEN" rather than "OAUTH2_TOKEN". Users who
		// want the all-caps form should write the field as "OAUTHToken"
		// or supply an explicit env:"NAME" tag.
		{"OAuth2Token", "O_AUTH2_TOKEN"},
		{"OAUTHToken", "OAUTH_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := toScreamingSnake(tc.in); got != tc.want {
				t.Errorf("toScreamingSnake(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseFieldTag_Empty(t *testing.T) {
	tag, err := parseFieldTag("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tag != (fieldTag{}) {
		t.Errorf("empty tag did not produce zero fieldTag: %+v", tag)
	}
}

func TestParseFieldTag_NameOnly(t *testing.T) {
	tag, err := parseFieldTag("FOO")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tag.Name != "FOO" {
		t.Errorf("Name = %q, want %q", tag.Name, "FOO")
	}
}

func TestParseFieldTag_NoNameWithOptions(t *testing.T) {
	tag, err := parseFieldTag(",required,notEmpty")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tag.Name != "" {
		t.Errorf("Name = %q, want empty", tag.Name)
	}
	if !tag.Required || !tag.NotEmpty {
		t.Errorf("flags not set: %+v", tag)
	}
}

func TestParseFieldTag_AllFlagOptions(t *testing.T) {
	tag, err := parseFieldTag("FOO,required,notEmpty,expand,fromFile,unset,secret,immutable,base64,hex")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := fieldTag{
		Name:      "FOO",
		Required:  true,
		NotEmpty:  true,
		Expand:    true,
		FromFile:  true,
		Unset:     true,
		Secret:    true,
		Immutable: true,
		Base64:    true,
		Hex:       true,
	}
	if tag != want {
		t.Errorf("got %+v\nwant %+v", tag, want)
	}
}

func TestParseFieldTag_KeyValueOptions(t *testing.T) {
	tag, err := parseFieldTag("FOO,default=8080,separator=;,kvSeparator==,layout=2006-01-02")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tag.Name != "FOO" {
		t.Errorf("Name = %q", tag.Name)
	}
	if !tag.HasDefault || tag.DefaultValue != "8080" {
		t.Errorf("default: HasDefault=%v Value=%q", tag.HasDefault, tag.DefaultValue)
	}
	if tag.Separator != ";" {
		t.Errorf("Separator = %q, want %q", tag.Separator, ";")
	}
	if tag.KVSeparator != "=" {
		t.Errorf("KVSeparator = %q, want %q", tag.KVSeparator, "=")
	}
	if tag.Layout != "2006-01-02" {
		t.Errorf("Layout = %q, want %q", tag.Layout, "2006-01-02")
	}
}

func TestParseFieldTag_DefaultEmpty(t *testing.T) {
	tag, err := parseFieldTag("FOO,default=")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !tag.HasDefault {
		t.Error("HasDefault=false; expected true even for empty default")
	}
	if tag.DefaultValue != "" {
		t.Errorf("DefaultValue = %q, want empty", tag.DefaultValue)
	}
}

func TestParseFieldTag_UnknownOption(t *testing.T) {
	_, err := parseFieldTag("FOO,bogus")
	if !errors.Is(err, ErrUnknownTagOption) {
		t.Errorf("err = %v, want ErrUnknownTagOption", err)
	}
}

func TestParseFieldTag_UnknownKeyValueOption(t *testing.T) {
	_, err := parseFieldTag("FOO,bogus=value")
	if !errors.Is(err, ErrUnknownTagOption) {
		t.Errorf("err = %v, want ErrUnknownTagOption", err)
	}
}

func TestParseFieldTag_TrimmedOptions(t *testing.T) {
	tag, err := parseFieldTag("FOO, required , notEmpty ")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !tag.Required || !tag.NotEmpty {
		t.Errorf("flags not set after trim: %+v", tag)
	}
}

func TestParseFieldTag_EmptyOptionSkipped(t *testing.T) {
	// Multiple commas (or whitespace-only options) produce empty option
	// strings after Split + TrimSpace; these are silently skipped.
	tag, err := parseFieldTag("FOO,,required, ,")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tag.Name != "FOO" || !tag.Required {
		t.Errorf("got %+v, want Name=FOO Required=true", tag)
	}
}

// --- field collection ---

type basicConfig struct {
	Port    int           `env:"PORT,default=8080" envDesc:"HTTP listen port"`
	DBURL   string        `env:"DATABASE_URL,required"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"`
	Debug   bool          // no tag → DEBUG via name transform
	Hidden  string        `env:"-"`
	private string        //nolint:unused // deliberate fixture: walker must skip unexported fields
}

func TestCollectStructInfo_Basic(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[basicConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}

	keys := fieldKeys(info.Fields)
	want := []string{"PORT", "DATABASE_URL", "TIMEOUT", "DEBUG"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}

	// Spot-check Port: default + description
	port := mustField(t, info.Fields, "PORT")
	if !port.HasDefault || port.DefaultValue != "8080" {
		t.Errorf("PORT default: HasDefault=%v Value=%q", port.HasDefault, port.DefaultValue)
	}
	if port.Description != "HTTP listen port" {
		t.Errorf("PORT description: %q", port.Description)
	}
	if port.GoName != "Port" {
		t.Errorf("GoName = %q", port.GoName)
	}
	if port.GoPath != "Port" {
		t.Errorf("GoPath = %q", port.GoPath)
	}
	if !slices.Equal(port.Index, []int{0}) {
		t.Errorf("Index = %v", port.Index)
	}

	// Required flag on DBURL
	db := mustField(t, info.Fields, "DATABASE_URL")
	if !db.Required {
		t.Error("DATABASE_URL not marked required")
	}

	// DEBUG comes from the SCREAMING_SNAKE_CASE transform of "Debug".
	debug := mustField(t, info.Fields, "DEBUG")
	if debug.GoName != "Debug" {
		t.Errorf("Debug GoName = %q", debug.GoName)
	}
}

type dbConfig struct {
	URL  string `env:"URL,required"`
	Pool int    `env:"POOL,default=5"`
}

type httpConfig struct {
	Port    int           `env:"PORT,default=8080"`
	Timeout time.Duration `env:"TIMEOUT,default=30s"`
}

type nestedConfig struct {
	HTTP httpConfig `envPrefix:"HTTP_"`
	DB   dbConfig   `envPrefix:"DB_"`
}

func TestCollectStructInfo_NestedWithPrefix(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[nestedConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}

	keys := fieldKeys(info.Fields)
	want := []string{"HTTP_PORT", "HTTP_TIMEOUT", "DB_URL", "DB_POOL"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}

	port := mustField(t, info.Fields, "HTTP_PORT")
	if port.GoPath != "HTTP.Port" {
		t.Errorf("HTTP_PORT GoPath = %q", port.GoPath)
	}
	if !slices.Equal(port.Index, []int{0, 0}) {
		t.Errorf("HTTP_PORT Index = %v, want [0 0]", port.Index)
	}
	dbURL := mustField(t, info.Fields, "DB_URL")
	if !slices.Equal(dbURL.Index, []int{1, 0}) {
		t.Errorf("DB_URL Index = %v, want [1 0]", dbURL.Index)
	}
}

type doublyNestedConfig struct {
	Outer struct {
		Inner struct {
			Name string `env:"NAME"`
		} `envPrefix:"INNER_"`
	} `envPrefix:"OUTER_"`
}

func TestCollectStructInfo_NestedPrefixesAccumulate(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[doublyNestedConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	if len(info.Fields) != 1 {
		t.Fatalf("got %d fields, want 1: %+v", len(info.Fields), info.Fields)
	}
	got := info.Fields[0].Key
	if got != "OUTER_INNER_NAME" {
		t.Errorf("Key = %q, want OUTER_INNER_NAME", got)
	}
}

type embedBase struct {
	Shared int `env:"SHARED,default=1"`
}

type embedTop struct {
	embedBase
	Custom string `env:"CUSTOM"`
}

func TestCollectStructInfo_AnonymousEmbed(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[embedTop]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"SHARED", "CUSTOM"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}
	shared := mustField(t, info.Fields, "SHARED")
	// Anonymous embed promotes fields; the GoPath should be just "Shared"
	// (not "embedBase.Shared").
	if shared.GoPath != "Shared" {
		t.Errorf("Shared GoPath = %q, want Shared", shared.GoPath)
	}
}

type embedTopWithPrefix struct {
	embedBase `envPrefix:"BASE_"`
	Custom    string `env:"CUSTOM"`
}

func TestCollectStructInfo_AnonymousEmbedWithPrefix(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[embedTopWithPrefix]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"BASE_SHARED", "CUSTOM"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}
}

type pointerNested struct {
	HTTP *httpConfig `envPrefix:"HTTP_"`
}

func TestCollectStructInfo_PointerToNestedStruct(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[pointerNested]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"HTTP_PORT", "HTTP_TIMEOUT"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}
}

type leafTypesConfig struct {
	Stamp time.Time      `env:"STAMP"`
	URL   url.URL        `env:"URL"`
	Net   net.IPNet      `env:"NET"`
	Re    regexp.Regexp  `env:"RE"`
	Empty *regexp.Regexp `env:"EMPTY"`
}

func TestCollectStructInfo_BuiltinLeafTypes(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[leafTypesConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"STAMP", "URL", "NET", "RE", "EMPTY"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v\n(Built-in leaf types must NOT be recursed into)", keys, want)
	}
}

// leafTypesUntaggedConfig exercises the leaf-detection path inside the
// walker's recursion check: with no env tag, the walker calls
// isLeafStruct(ft) to decide between recursing and treating the field as
// a leaf. Each field type below MUST be detected as a leaf so the resolved
// keys come from the SCREAMING_SNAKE_CASE transform of the Go field name,
// not from recursing into the struct's exported fields (which would
// produce keys like "WALL", "EXT", "LOC" for time.Time).
type leafTypesUntaggedConfig struct {
	Stamp time.Time
	Loc   time.Location
	URL   url.URL
	Net   net.IPNet
	Re    regexp.Regexp
	Int   big.Int
	Float big.Float
	Rat   big.Rat
}

func TestCollectStructInfo_BuiltinLeafTypesUntagged(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[leafTypesUntaggedConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"STAMP", "LOC", "URL", "NET", "RE", "INT", "FLOAT", "RAT"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}
}

// customDecodable implements encoding.TextUnmarshaler via a pointer
// receiver, so the walker's interface-based leaf detection should treat
// any field of this type as a leaf even though the type isn't one of the
// built-in leaves.
type customDecodable struct {
	raw string
}

func (c *customDecodable) UnmarshalText(b []byte) error {
	c.raw = string(b)
	return nil
}

type customUnmarshalerConfig struct {
	Field customDecodable
}

func TestCollectStructInfo_CustomTextUnmarshalerIsLeaf(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[customUnmarshalerConfig]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"FIELD"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v\nwant %v\n(custom TextUnmarshaler must be treated as a leaf)", keys, want)
	}
}

// myUnexportedAlias is an unexported named type so that an anonymous embed
// of it appears as an unexported, non-struct anonymous field. The walker
// must skip such fields without error.
type myUnexportedAlias int //nolint:unused

type withEmbeddedUnexportedNonStruct struct {
	myUnexportedAlias        //nolint:unused // deliberate fixture: walker must skip this
	Foo               string `env:"FOO"`
}

func TestCollectStructInfo_EmbeddedUnexportedNonStruct(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[withEmbeddedUnexportedNonStruct]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"FOO"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

type conflictConfig struct {
	A string `env:"FOO"`
	B string `env:"FOO"`
}

func TestCollectStructInfo_Conflict(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[conflictConfig]())
	if info.Err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(info.Err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", info.Err)
	}
}

type conflictAcrossNestingConfig struct {
	A string              `env:"DB_URL"`
	B nestedConfigOnlyURL `envPrefix:"DB_"`
}

type nestedConfigOnlyURL struct {
	URL string `env:"URL"`
}

func TestCollectStructInfo_ConflictAcrossNesting(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[conflictAcrossNestingConfig]())
	if info.Err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(info.Err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", info.Err)
	}
}

type bogusTagConfig struct {
	A string `env:"FOO,bogus"`
}

func TestCollectStructInfo_BogusTag(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[bogusTagConfig]())
	if info.Err == nil {
		t.Fatal("expected unknown-tag-option error")
	}
	if !errors.Is(info.Err, ErrUnknownTagOption) {
		t.Errorf("err = %v, want ErrUnknownTagOption", info.Err)
	}
}

func TestCollectStructInfo_NotAStruct(t *testing.T) {
	info := getStructInfo(reflect.TypeFor[int]())
	if !errors.Is(info.Err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", info.Err)
	}
}

func TestCollectStructInfo_EmptyStruct(t *testing.T) {
	type Empty struct{}
	info := getStructInfo(reflect.TypeFor[Empty]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	if len(info.Fields) != 0 {
		t.Errorf("got %d fields, want 0", len(info.Fields))
	}
}

func TestCollectStructInfo_NoTagFieldsExported(t *testing.T) {
	type Plain struct {
		Foo int
		Bar string
	}
	info := getStructInfo(reflect.TypeFor[Plain]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	keys := fieldKeys(info.Fields)
	want := []string{"FOO", "BAR"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestCollectStructInfo_AllOptionsRecorded(t *testing.T) {
	type Cfg struct {
		Field string `env:"FIELD,required,notEmpty,default=fallback,separator=;,kvSeparator==,expand,fromFile,unset,secret,immutable,layout=2006-01-02,base64,hex"`
	}
	info := getStructInfo(reflect.TypeFor[Cfg]())
	if info.Err != nil {
		t.Fatalf("unexpected err: %v", info.Err)
	}
	if len(info.Fields) != 1 {
		t.Fatalf("got %d fields, want 1", len(info.Fields))
	}
	f := info.Fields[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Required", f.Required, true},
		{"NotEmpty", f.NotEmpty, true},
		{"HasDefault", f.HasDefault, true},
		{"DefaultValue", f.DefaultValue, "fallback"},
		{"Separator", f.Separator, ";"},
		{"KVSeparator", f.KVSeparator, "="},
		{"Expand", f.Expand, true},
		{"FromFile", f.FromFile, true},
		{"Unset", f.Unset, true},
		{"Secret", f.Secret, true},
		{"Immutable", f.Immutable, true},
		{"Layout", f.Layout, "2006-01-02"},
		{"Base64", f.Base64, true},
		{"Hex", f.Hex, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

type cacheRoot struct {
	A int    `env:"A"`
	B string `env:"B"`
}

func TestStructInfoCache_ReturnsSameInstance(t *testing.T) {
	a := getStructInfo(reflect.TypeFor[cacheRoot]())
	b := getStructInfo(reflect.TypeFor[cacheRoot]())
	if a != b {
		t.Errorf("cache returned different instances: %p vs %p", a, b)
	}
}

func TestStructInfoCache_Concurrent(t *testing.T) {
	// Rotate among several types so we exercise both fresh-population and
	// already-cached paths concurrently.
	type T1 struct{ X int }
	type T2 struct{ Y string }
	type T3 struct{ Z bool }

	types := []reflect.Type{
		reflect.TypeFor[T1](),
		reflect.TypeFor[T2](),
		reflect.TypeFor[T3](),
	}

	const goroutines = 16
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(g int) {
			defer wg.Done()
			for i := range iterations {
				typ := types[(g+i)%len(types)]
				info := getStructInfo(typ)
				if info.Err != nil {
					t.Errorf("err for %s: %v", typ, info.Err)
					return
				}
				if len(info.Fields) != 1 {
					t.Errorf("%s: %d fields, want 1", typ, len(info.Fields))
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// --- helpers ---

func fieldKeys(fields []fieldDescriptor) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Key
	}
	return out
}

func mustField(t *testing.T, fields []fieldDescriptor, key string) fieldDescriptor {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no field with key %q in %v", key, fieldKeys(fields))
	return fieldDescriptor{}
}

type recursiveNode struct {
	Name string
	Next *recursiveNode
}

type recursiveOuter struct {
	Inner struct {
		Back *recursiveOuter
	}
}

type recursiveEmbed struct {
	*recursiveEmbed //nolint:unused // only its type matters to the walk
	Port            int
}

func TestWalk_RecursiveTypeIsAnError(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]any{
		"self pointer":  &recursiveNode{},
		"through child": &recursiveOuter{},
		"embedded":      &recursiveEmbed{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := New(WithSource(Map(nil)))
			if err := l.Load(v); !errors.Is(err, ErrRecursiveType) {
				t.Errorf("Load = %v, want ErrRecursiveType", err)
			}
			if err := l.PrintUsage(io.Discard, v); !errors.Is(err, ErrRecursiveType) {
				t.Errorf("PrintUsage = %v, want ErrRecursiveType", err)
			}
			if err := Encode(io.Discard, v); !errors.Is(err, ErrRecursiveType) {
				t.Errorf("Encode = %v, want ErrRecursiveType", err)
			}
		})
	}
}

func TestWalk_RecursiveFieldSkippedWithDash(t *testing.T) {
	t.Parallel()
	type node struct {
		Name string         `env:"NAME"`
		Next *recursiveNode `env:"-"`
	}
	var n node
	if err := New(WithSource(Map(map[string]string{"NAME": "a"}))).Load(&n); err != nil || n.Name != "a" {
		t.Fatalf("Load = %v, %+v", err, n)
	}
}

type tagEmbedded struct{ Z int }

func TestWalk_EnvTagOnEmbeddedStructIgnored(t *testing.T) {
	t.Parallel()
	var c struct {
		tagEmbedded `env:"EMB,required"`
	}
	if err := New(WithSource(Map(map[string]string{"Z": "7"}))).Load(&c); err != nil || c.Z != 7 {
		t.Errorf("Load = %v, Z = %d; want nil, 7", err, c.Z)
	}
}

type (
	selfMap   map[string]selfMap
	selfSlice []selfSlice
	selfArray [2]*selfArray
)

func TestWalk_SelfReferencingContainerTypes(t *testing.T) {
	t.Parallel()
	type cfg struct {
		M selfMap   `env:"M"`
		S selfSlice `env:"S"`
		A selfArray `env:"A"`
		K map[*selfArray]Secret[string]
	}
	if isSecretType(reflect.TypeFor[selfMap]()) || isSecretType(reflect.TypeFor[selfArray]()) {
		t.Error("isSecretType = true for a self-referencing type without Secret")
	}
	if !isSecretType(reflect.TypeFor[map[*selfArray]Secret[string]]()) {
		t.Error("isSecretType = false for a map of Secret")
	}
	var c cfg
	l := New(WithSource(Map(nil)))
	if err := l.Load(&c); err != nil {
		t.Fatalf("Load = %v", err)
	}
	if docs := l.Describe(&c); len(docs) != 4 {
		t.Errorf("Describe = %d rows, want 4", len(docs))
	}
	// The nil *selfArray elements have no text form; Encode must still
	// return.
	if err := l.Encode(io.Discard, &c); err != nil && !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("Encode = %v", err)
	}
}
