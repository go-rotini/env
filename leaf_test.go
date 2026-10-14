package env

import (
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsLeafStruct_BuiltinTypes(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[time.Time](),
		reflect.TypeFor[time.Location](),
		reflect.TypeFor[url.URL](),
		reflect.TypeFor[net.IPNet](),
		reflect.TypeFor[regexp.Regexp](),
		reflect.TypeFor[big.Int](),
		reflect.TypeFor[big.Float](),
		reflect.TypeFor[big.Rat](),
	} {
		if !isLeafStruct(typ, "env") {
			t.Errorf("%s is not a leaf", typ)
		}
	}
	if isLeafStruct(reflect.TypeFor[struct{ A int }](), "env") {
		t.Error("plain struct treated as a leaf")
	}
}

func TestLoad_BuiltinLeafTypes(t *testing.T) {
	var cfg struct {
		Stamp time.Time
		Loc   *time.Location
		URL   url.URL
		Net   net.IPNet
		Re    regexp.Regexp
		Int   big.Int
		Float big.Float
		Rat   big.Rat
	}
	src := Map(map[string]string{
		"STAMP": "2026-01-02T03:04:05Z",
		"LOC":   "UTC",
		"URL":   "https://example.com/a",
		"NET":   "10.0.0.0/8",
		"RE":    "^a+$",
		"INT":   "123456789012345678901234567890",
		"FLOAT": "1.5",
		"RAT":   "3/4",
	})
	if err := New(WithSource(src)).Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Stamp.Year() != 2026 || cfg.Loc.String() != "UTC" || cfg.URL.Host != "example.com" ||
		cfg.Net.String() != "10.0.0.0/8" || !cfg.Re.MatchString("aaa") {
		t.Errorf("stdlib leaves = %+v", cfg)
	}
	if cfg.Int.String() != "123456789012345678901234567890" || cfg.Float.String() != "1.5" ||
		cfg.Rat.String() != "3/4" {
		t.Errorf("math/big leaves = %s %s %s", cfg.Int.String(), cfg.Float.String(), cfg.Rat.String())
	}
}

// leafDB has an UnmarshalEnv shorthand and env-tagged fields: it is
// walked field by field, not loaded as one value.
type leafDB struct {
	Host string `env:"HOST"`
	Port int    `env:"PORT"`
}

func (d *leafDB) UnmarshalEnv(s string) error {
	h, p, _ := strings.Cut(s, ":")
	n, err := strconv.Atoi(p)
	d.Host, d.Port = h, n
	return err
}

// leafLevel has an UnmarshalEnv method and no loadable fields: it is
// loaded as one value.
type leafLevel struct{ v string }

func (l *leafLevel) UnmarshalEnv(s string) error { l.v = s; return nil }

// LeafLevel is exported so an embed of it is a loadable field.
type LeafLevel = leafLevel

// leafLogging embeds a type with UnmarshalEnv, which promotes the
// method, and has fields of its own.
type leafLogging struct {
	LeafLevel
	File string `env:"FILE"`
}

func TestLoad_UnmarshalerStructWithFieldsIsWalked(t *testing.T) {
	var cfg struct {
		DB  leafDB      `envPrefix:"DB_"`
		Log leafLogging `envPrefix:"LOG_"`
		Lvl leafLevel
	}
	src := Map(map[string]string{
		"DB_HOST": "h", "DB_PORT": "5432",
		"LOG_FILE": "/x", "LOG_LEAF_LEVEL": "debug",
		"LVL": "info",
	})
	if err := New(WithSource(src)).Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DB.Host != "h" || cfg.DB.Port != 5432 {
		t.Errorf("DB = %+v, want fields loaded from DB_HOST and DB_PORT", cfg.DB)
	}
	if cfg.Log.File != "/x" || cfg.Log.v != "debug" {
		t.Errorf("Log = %+v, want File and the embedded level loaded", cfg.Log)
	}
	if cfg.Lvl.v != "info" {
		t.Errorf("Lvl = %+v, want loaded through UnmarshalEnv", cfg.Lvl)
	}
	var names []string
	for _, d := range Describe(&cfg) {
		names = append(names, d.Name)
	}
	want := []string{"DB_HOST", "DB_PORT", "LOG_LEAF_LEVEL", "LOG_FILE", "LVL"}
	if !slices.Equal(names, want) {
		t.Errorf("Describe names = %v, want %v", names, want)
	}
}

// leafCreds embeds a Secret, which promotes its methods, and has a field
// of its own: it is walked, not taken for a Secret.
type leafCreds struct {
	Secret[string]
	User string
}

func TestLoad_EmbeddedSecretStructIsWalked(t *testing.T) {
	var cfg struct {
		DB leafCreds `envPrefix:"DB_"`
	}
	src := Map(map[string]string{"DB_USER": "u", "DB_SECRET": "s3cr3t"})
	if err := New(WithSource(src)).Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DB.User != "u" || cfg.DB.Value() != "s3cr3t" {
		t.Errorf("DB = {User: %q, Secret: %q}, want both loaded", cfg.DB.User, cfg.DB.Value())
	}
	var names []string
	for _, d := range Describe(&cfg) {
		names = append(names, d.Name)
		if d.Name == "DB_SECRET" && !d.Secret {
			t.Error("the embedded Secret is not marked secret")
		}
	}
	if want := []string{"DB_SECRET", "DB_USER"}; !slices.Equal(names, want) {
		t.Errorf("Describe names = %v, want %v", names, want)
	}
	if isSecretType(reflect.TypeFor[leafCreds]()) {
		t.Error("a struct embedding a Secret is taken for a Secret")
	}
}

// The deepest of a chain of embeds has UnmarshalEnv, which every level
// above it inherits.
type (
	chain0 struct {
		LeafLevel
		S string
	}
	chain1  struct{ chain0 }
	chain2  struct{ chain1 }
	chain3  struct{ chain2 }
	chain4  struct{ chain3 }
	chain5  struct{ chain4 }
	chain6  struct{ chain5 }
	chain7  struct{ chain6 }
	chain8  struct{ chain7 }
	chain9  struct{ chain8 }
	chain10 struct{ chain9 }
	chain11 struct{ chain10 }
	chain12 struct{ chain11 }
	chain13 struct{ chain12 }
	chain14 struct{ chain13 }
	chain15 struct{ chain14 }
	chain16 struct{ chain15 }
	chain17 struct{ chain16 }
	chain18 struct{ chain17 }
	chain19 struct{ chain18 }
	chain20 struct{ chain19 }
)

func TestIsLeafStruct_DeepUnmarshalerChainIsPolynomial(t *testing.T) {
	typ := reflect.TypeFor[chain20]()
	// Each level is checked once, so allocations grow with the depth;
	// checking each level twice would take about 2²⁰.
	allocs := testing.AllocsPerRun(1, func() {
		if isLeafStruct(typ, "env") {
			t.Error("chain20 is a leaf")
		}
	})
	if allocs > 2_000 {
		t.Errorf("isLeafStruct allocated %.0f times for a 20-level chain", allocs)
	}
}
