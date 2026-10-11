package env

import (
	"math/big"
	"net"
	"net/url"
	"reflect"
	"regexp"
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
		if !isLeafStruct(typ) {
			t.Errorf("%s is not a leaf", typ)
		}
	}
	if isLeafStruct(reflect.TypeFor[struct{ A int }]()) {
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
