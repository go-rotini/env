package env

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// encodeToString is a helper that runs Encode and returns the output as
// a string for line-oriented assertions.
func encodeToString(t *testing.T, v any, opts ...EncodeOption) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, v, opts...); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	return buf.String()
}

// assertOwnerOnly checks that path is not group/other-accessible. It is
// a no-op on Windows, which has no Unix permission bits — os.Stat
// reports 0666 there regardless of the mode passed to OpenFile.
func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("perm %o exposes group/other access, want owner-only", perm)
	}
}

// --- happy path ---

type encodeBasicConfig struct {
	Port    int           `env:"PORT,default=8080"`
	DBURL   string        `env:"DATABASE_URL"`
	Timeout time.Duration `env:"TIMEOUT"`
	Debug   bool
}

func TestEncode_HappyPath(t *testing.T) {
	cfg := encodeBasicConfig{
		Port:    9000,
		DBURL:   "postgres://localhost",
		Timeout: 30 * time.Second,
		Debug:   true,
	}
	out := encodeToString(t, cfg)
	want := "PORT=9000\nDATABASE_URL=postgres://localhost\nTIMEOUT=30s\nDEBUG=true\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestEncode_AcceptsPointer(t *testing.T) {
	cfg := encodeBasicConfig{Port: 1, DBURL: "x", Debug: false}
	out := encodeToString(t, &cfg)
	if !strings.Contains(out, "PORT=1") {
		t.Errorf("pointer form failed: %q", out)
	}
}

func TestEncode_NilTarget(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestEncode_NilPointer(t *testing.T) {
	var buf bytes.Buffer
	var cfg *encodeBasicConfig
	if err := Encode(&buf, cfg); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestEncode_NotStruct(t *testing.T) {
	var buf bytes.Buffer
	x := 42
	if err := Encode(&buf, &x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

func TestEncode_StructInfoError(t *testing.T) {
	type cfg struct {
		A string `env:"FOO"`
		B string `env:"FOO"` // conflict
	}
	var buf bytes.Buffer
	if err := Encode(&buf, cfg{}); !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", err)
	}
}

// --- quoting ---

func TestEncode_QuotingTriggers(t *testing.T) {
	type cfg struct {
		Plain   string `env:"PLAIN"`
		Spaces  string `env:"SPACES"`
		Hash    string `env:"HASH"`
		Dollar  string `env:"DOLLAR"`
		Quote   string `env:"QUOTE"`
		Newline string `env:"NEWLINE"`
		Empty   string `env:"EMPTY"`
	}
	c := cfg{
		Plain:   "value",
		Spaces:  "with spaces",
		Hash:    "has#hash",
		Dollar:  "has$dollar",
		Quote:   `has"quote`,
		Newline: "line1\nline2",
		Empty:   "",
	}
	out := encodeToString(t, c)
	checks := []struct {
		name, want string
	}{
		{"plain bare", "PLAIN=value\n"},
		{"spaces quoted", `SPACES="with spaces"`},
		{"hash quoted", `HASH="has#hash"`},
		{"dollar escaped", `DOLLAR="has\$dollar"`},
		{"quote escaped", `QUOTE="has\"quote"`},
		{"newline escaped", `NEWLINE="line1\nline2"`},
		{"empty bare", "EMPTY=\n"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: missing %q in output:\n%s", c.name, c.want, out)
		}
	}
}

func TestEncode_QuoteAll(t *testing.T) {
	type cfg struct {
		A string `env:"A"`
		B int    `env:"B"`
	}
	out := encodeToString(t, cfg{A: "plain", B: 42}, WithEncodeQuoteAll(true))
	if !strings.Contains(out, `A="plain"`) {
		t.Errorf("string not quoted: %q", out)
	}
	if !strings.Contains(out, `B="42"`) {
		t.Errorf("int not quoted: %q", out)
	}
}

func TestEncode_QuoteEscapes(t *testing.T) {
	type cfg struct {
		X string `env:"X"`
	}
	c := cfg{X: "tab\there\nbackslash\\and\\bell\b\fff\rcr"}
	out := encodeToString(t, c)
	want := `X="tab\there\nbackslash\\and\\bell\b\fff\rcr"`
	if !strings.Contains(out, want) {
		t.Errorf("got %q\nwant substring %q", out, want)
	}
}

// --- export prefix ---

func TestEncode_Export(t *testing.T) {
	type cfg struct {
		A string `env:"A"`
	}
	out := encodeToString(t, cfg{A: "v"}, WithEncodeExport(true))
	if out != "export A=v\n" {
		t.Errorf("got %q", out)
	}
}

// --- secret redaction ---

type encodeSecretConfig struct {
	APIKey string `env:"API_KEY,secret"`
	Token  string `env:"TOKEN,secret"`
	Plain  string `env:"PLAIN"`
}

func TestEncode_SecretsRedactedByDefault(t *testing.T) {
	c := encodeSecretConfig{APIKey: "real-key", Token: "real-token", Plain: "ok"}
	out := encodeToString(t, c)
	if strings.Contains(out, "real-key") || strings.Contains(out, "real-token") {
		t.Errorf("secret value leaked: %q", out)
	}
	if !strings.Contains(out, "API_KEY=***") {
		t.Errorf("API_KEY not redacted: %q", out)
	}
	if !strings.Contains(out, "PLAIN=ok") {
		t.Errorf("non-secret was incorrectly redacted: %q", out)
	}
}

func TestEncode_IncludeSecrets(t *testing.T) {
	c := encodeSecretConfig{APIKey: "real-key", Plain: "ok"}
	out := encodeToString(t, c, WithEncodeIncludeSecrets(true))
	if !strings.Contains(out, "API_KEY=real-key") {
		t.Errorf("secret should be emitted: %q", out)
	}
}

// --- comments ---

type encodeWithDescConfig struct {
	Port  int    `env:"PORT,default=8080" envDesc:"HTTP listen port"`
	Plain string `env:"PLAIN"`
}

func TestEncode_DescTagEmittedAsComment(t *testing.T) {
	out := encodeToString(t, encodeWithDescConfig{Port: 8080, Plain: "x"})
	if !strings.Contains(out, "# HTTP listen port\nPORT=8080") {
		t.Errorf("desc not emitted as comment:\n%s", out)
	}
}

func TestEncode_MultilineDesc(t *testing.T) {
	type cfg struct {
		X int `env:"X" envDesc:"line one\nline two\nline three"`
	}
	out := encodeToString(t, cfg{X: 1})
	for _, want := range []string{"# line one", "# line two", "# line three"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestEncode_CommentsOption(t *testing.T) {
	c := encodeWithDescConfig{Port: 8080, Plain: "x"}
	out := encodeToString(t, c, WithEncodeComments(map[string]string{
		"PORT": "Custom override",
	}))
	if !strings.Contains(out, "# Custom override\nPORT=8080") {
		t.Errorf("comment override not applied:\n%s", out)
	}
	if strings.Contains(out, "HTTP listen port") {
		t.Errorf("original desc was not replaced:\n%s", out)
	}
}

// --- sort ---

type encodeSortConfig struct {
	Zebra int `env:"ZEBRA"`
	Alpha int `env:"ALPHA"`
	Mango int `env:"MANGO"`
}

func TestEncode_DefaultOrder(t *testing.T) {
	out := encodeToString(t, encodeSortConfig{Zebra: 1, Alpha: 2, Mango: 3})
	idxZ := strings.Index(out, "ZEBRA=")
	idxA := strings.Index(out, "ALPHA=")
	idxM := strings.Index(out, "MANGO=")
	if !(idxZ < idxA && idxA < idxM) {
		t.Errorf("default order not declaration-order:\n%s", out)
	}
}

func TestEncode_Sort(t *testing.T) {
	out := encodeToString(t, encodeSortConfig{Zebra: 1, Alpha: 2, Mango: 3}, WithEncodeSort(true))
	idxA := strings.Index(out, "ALPHA=")
	idxM := strings.Index(out, "MANGO=")
	idxZ := strings.Index(out, "ZEBRA=")
	if !(idxA < idxM && idxM < idxZ) {
		t.Errorf("WithEncodeSort did not sort lexicographically:\n%s", out)
	}
}

// --- nil omission ---

type encodePointerOmitConfig struct {
	Always int            `env:"ALWAYS"`
	Maybe  *int           `env:"MAYBE"`
	Slice  []int          `env:"SLICE"`
	Map    map[string]int `env:"MAP"`
}

func TestEncode_NilPointerOmitted(t *testing.T) {
	c := encodePointerOmitConfig{Always: 1}
	out := encodeToString(t, c)
	if !strings.Contains(out, "ALWAYS=1") {
		t.Error("ALWAYS missing")
	}
	if strings.Contains(out, "MAYBE=") {
		t.Errorf("nil pointer should be omitted:\n%s", out)
	}
	if strings.Contains(out, "SLICE=") {
		t.Errorf("nil slice should be omitted:\n%s", out)
	}
	if strings.Contains(out, "MAP=") {
		t.Errorf("nil map should be omitted:\n%s", out)
	}
}

func TestEncode_PointerWithValue(t *testing.T) {
	v := 42
	c := encodePointerOmitConfig{Always: 1, Maybe: &v}
	out := encodeToString(t, c)
	if !strings.Contains(out, "MAYBE=42") {
		t.Errorf("non-nil pointer not emitted:\n%s", out)
	}
}

func TestEncode_EmptySliceEmittedAsBlank(t *testing.T) {
	type cfg struct {
		Hosts []string `env:"HOSTS"`
	}
	c := cfg{Hosts: []string{}} // non-nil, zero-len
	out := encodeToString(t, c)
	if !strings.Contains(out, "HOSTS=\n") {
		t.Errorf("empty slice should emit empty value, got:\n%s", out)
	}
}

func TestEncode_EmptyMapEmittedAsBlank(t *testing.T) {
	type cfg struct {
		Tags map[string]string `env:"TAGS"`
	}
	c := cfg{Tags: map[string]string{}}
	out := encodeToString(t, c)
	if !strings.Contains(out, "TAGS=\n") {
		t.Errorf("empty map should emit empty value, got:\n%s", out)
	}
}

// --- nested struct (intermediate nil) ---

type encodeHTTP struct {
	Port int `env:"PORT"`
}

type encodeOuter struct {
	HTTP *encodeHTTP `envPrefix:"HTTP_"`
	Top  string      `env:"TOP"`
}

func TestEncode_NilNestedPointerOmitsLeaves(t *testing.T) {
	c := encodeOuter{Top: "x"} // HTTP nil
	out := encodeToString(t, c)
	if strings.Contains(out, "HTTP_PORT=") {
		t.Errorf("leaf under nil pointer should be omitted:\n%s", out)
	}
	if !strings.Contains(out, "TOP=x") {
		t.Errorf("sibling field missing:\n%s", out)
	}
}

func TestEncode_PopulatedNestedPointer(t *testing.T) {
	c := encodeOuter{HTTP: &encodeHTTP{Port: 9000}, Top: "x"}
	out := encodeToString(t, c)
	if !strings.Contains(out, "HTTP_PORT=9000") {
		t.Errorf("nested leaf missing:\n%s", out)
	}
}

// --- type rendering ---

func TestEncode_AllPrimitives(t *testing.T) {
	type cfg struct {
		S   string  `env:"S"`
		B   bool    `env:"B"`
		I   int     `env:"I"`
		I8  int8    `env:"I8"`
		I64 int64   `env:"I64"`
		U   uint    `env:"U"`
		U8  uint8   `env:"U8"`
		F32 float32 `env:"F32"`
		F64 float64 `env:"F64"`
	}
	c := cfg{S: "x", B: true, I: -1, I8: 127, I64: 9223372036854775000, U: 1, U8: 255, F32: 3.14, F64: 1.5e10}
	out := encodeToString(t, c)
	for _, want := range []string{"S=x", "B=true", "I=-1", "I8=127", "I64=9223372036854775000", "U=1", "U8=255", "F32=3.14", "F64=1.5e+10"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestEncode_Slice(t *testing.T) {
	type cfg struct {
		Hosts []string `env:"HOSTS"`
		Nums  []int    `env:"NUMS"`
	}
	c := cfg{Hosts: []string{"a", "b", "c"}, Nums: []int{1, 2, 3}}
	out := encodeToString(t, c)
	if !strings.Contains(out, "HOSTS=a,b,c") {
		t.Errorf("slice rendering: %q", out)
	}
	if !strings.Contains(out, "NUMS=1,2,3") {
		t.Errorf("int slice: %q", out)
	}
}

func TestEncode_SliceCustomSeparator(t *testing.T) {
	type cfg struct {
		Hosts []string `env:"HOSTS,separator=;"`
	}
	out := encodeToString(t, cfg{Hosts: []string{"a", "b"}})
	if !strings.Contains(out, "HOSTS=a;b") {
		t.Errorf("custom separator not applied: %q", out)
	}
}

func TestEncode_FixedArray(t *testing.T) {
	type cfg struct {
		Triple [3]int `env:"TRIPLE"`
	}
	c := cfg{Triple: [3]int{10, 20, 30}}
	out := encodeToString(t, c)
	if !strings.Contains(out, "TRIPLE=10,20,30") {
		t.Errorf("array rendering: %q", out)
	}
}

func TestEncode_Map(t *testing.T) {
	type cfg struct {
		Tags map[string]string `env:"TAGS"`
	}
	c := cfg{Tags: map[string]string{"k1": "v1", "k2": "v2"}}
	out := encodeToString(t, c)
	// Map keys are sorted lexicographically for deterministic output.
	if !strings.Contains(out, "TAGS=k1:v1,k2:v2") {
		t.Errorf("map rendering: %q", out)
	}
}

func TestEncode_MapCustomSeparators(t *testing.T) {
	type cfg struct {
		Tags map[string]int `env:"TAGS,separator=;,kvSeparator=="`
	}
	c := cfg{Tags: map[string]int{"a": 1, "b": 2}}
	out := encodeToString(t, c)
	if !strings.Contains(out, "TAGS=") || !strings.Contains(out, "a=1;b=2") {
		t.Errorf("custom map separators: %q", out)
	}
}

func TestEncode_Duration(t *testing.T) {
	type cfg struct {
		T time.Duration `env:"T"`
	}
	out := encodeToString(t, cfg{T: 90 * time.Minute})
	if !strings.Contains(out, "T=1h30m0s") {
		t.Errorf("duration rendering: %q", out)
	}
}

func TestEncode_Time_DefaultRFC3339(t *testing.T) {
	type cfg struct {
		T time.Time `env:"T"`
	}
	tm := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	out := encodeToString(t, cfg{T: tm})
	if !strings.Contains(out, "T=2026-05-08T12:34:56Z") {
		t.Errorf("time rendering: %q", out)
	}
}

func TestEncode_Time_CustomLayout(t *testing.T) {
	type cfg struct {
		T time.Time `env:"T,layout=2006-01-02"`
	}
	tm := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	out := encodeToString(t, cfg{T: tm})
	if !strings.Contains(out, "T=2026-05-08\n") {
		t.Errorf("custom layout: %q", out)
	}
}

func TestEncode_Location(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tz database not available")
	}
	type cfg struct {
		L *time.Location `env:"L"`
	}
	out := encodeToString(t, cfg{L: loc})
	if !strings.Contains(out, "L=America/New_York") {
		t.Errorf("location: %q", out)
	}
}

func TestEncode_URL(t *testing.T) {
	u, _ := url.Parse("https://example.com/path")
	type cfg struct {
		U *url.URL `env:"U"`
	}
	out := encodeToString(t, cfg{U: u})
	if !strings.Contains(out, "U=https://example.com/path") {
		t.Errorf("URL: %q", out)
	}
}

func TestEncode_NetIP(t *testing.T) {
	type cfg struct {
		IP net.IP `env:"IP"`
	}
	out := encodeToString(t, cfg{IP: net.IPv4(10, 0, 0, 1)})
	if !strings.Contains(out, "IP=10.0.0.1") {
		t.Errorf("net.IP: %q", out)
	}
}

func TestEncode_NetIPNet(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/8")
	type cfg struct {
		Net *net.IPNet `env:"NET"`
	}
	out := encodeToString(t, cfg{Net: ipnet})
	if !strings.Contains(out, "NET=10.0.0.0/8") {
		t.Errorf("net.IPNet: %q", out)
	}
}

func TestEncode_Regexp(t *testing.T) {
	re := regexp.MustCompile(`^foo[0-9]+$`)
	type cfg struct {
		Re *regexp.Regexp `env:"RE"`
	}
	out := encodeToString(t, cfg{Re: re})
	if !strings.Contains(out, `RE=^foo[0-9]+$`) {
		// pattern contains $ so output should be quoted
		if !strings.Contains(out, `RE="^foo[0-9]+\$"`) {
			t.Errorf("regexp: %q", out)
		}
	}
}

func TestEncode_BigInt(t *testing.T) {
	type cfg struct {
		N big.Int `env:"N"`
	}
	bi := new(big.Int)
	bi.SetString("12345678901234567890", 10)
	out := encodeToString(t, cfg{N: *bi})
	if !strings.Contains(out, "N=12345678901234567890") {
		t.Errorf("big.Int: %q", out)
	}
}

// --- []byte / [N]byte ---

func TestEncode_ByteSlice_Raw(t *testing.T) {
	type cfg struct {
		B []byte `env:"B"`
	}
	out := encodeToString(t, cfg{B: []byte("hello")})
	if !strings.Contains(out, "B=hello") {
		t.Errorf("raw bytes: %q", out)
	}
}

func TestEncode_ByteSlice_Base64(t *testing.T) {
	type cfg struct {
		B []byte `env:"B,base64"`
	}
	out := encodeToString(t, cfg{B: []byte("hello")})
	if !strings.Contains(out, "B=aGVsbG8=") {
		t.Errorf("base64 bytes: %q", out)
	}
}

func TestEncode_ByteSlice_InvalidUTF8Preserved(t *testing.T) {
	// A []byte field whose content is not valid UTF-8 and needs quoting
	// (because of the leading space) must round-trip byte-for-byte — the
	// encoder iterates bytes, not runes, so 0xAA is not collapsed to U+FFFD.
	type cfg struct {
		B []byte `env:"B"`
	}
	val := []byte{' ', 'A', '=', 0xaa}
	out := encodeToString(t, cfg{B: val})
	if !strings.Contains(out, "B=\" A=\xaa\"") {
		t.Fatalf("encoded = %q, want a line `B=\" A=\\xaa\"`", out)
	}
}

func TestEncode_ByteSlice_Hex(t *testing.T) {
	type cfg struct {
		B []byte `env:"B,hex"`
	}
	out := encodeToString(t, cfg{B: []byte("hello")})
	if !strings.Contains(out, "B=68656c6c6f") {
		t.Errorf("hex bytes: %q", out)
	}
}

func TestEncode_ByteArray_Raw(t *testing.T) {
	type cfg struct {
		B [5]byte `env:"B"`
	}
	out := encodeToString(t, cfg{B: [5]byte{'h', 'e', 'l', 'l', 'o'}})
	if !strings.Contains(out, "B=hello") {
		t.Errorf("byte array raw: %q", out)
	}
}

func TestEncode_ByteArray_Hex(t *testing.T) {
	type cfg struct {
		B [5]byte `env:"B,hex"`
	}
	out := encodeToString(t, cfg{B: [5]byte{'h', 'e', 'l', 'l', 'o'}})
	if !strings.Contains(out, "B=68656c6c6f") {
		t.Errorf("byte array hex: %q", out)
	}
}

// --- Marshaler interface priority ---

type encodeMyValue struct {
	via string
}

func (m *encodeMyValue) MarshalEnv() (string, error)    { return "env:" + m.via, nil }
func (m *encodeMyValue) MarshalText() ([]byte, error)   { return []byte("text:" + m.via), nil }
func (m *encodeMyValue) MarshalBinary() ([]byte, error) { return []byte("binary:" + m.via), nil }

func TestEncode_PriorityChain_MarshalerWins(t *testing.T) {
	type cfg struct {
		V encodeMyValue `env:"V"`
	}
	out := encodeToString(t, cfg{V: encodeMyValue{via: "x"}})
	if !strings.Contains(out, "V=env:x") {
		t.Errorf("Marshaler should win over Text/Binary; got %q", out)
	}
}

type encodeText struct {
	v string
}

func (t *encodeText) MarshalText() ([]byte, error) { return []byte("text:" + t.v), nil }

func TestEncode_TextMarshaler(t *testing.T) {
	type cfg struct {
		V encodeText `env:"V"`
	}
	out := encodeToString(t, cfg{V: encodeText{v: "x"}})
	if !strings.Contains(out, "V=text:x") {
		t.Errorf("got %q", out)
	}
}

type encodeBinary struct {
	v string
}

func (b *encodeBinary) MarshalBinary() ([]byte, error) { return []byte("binary:" + b.v), nil }

func TestEncode_BinaryMarshaler(t *testing.T) {
	type cfg struct {
		V encodeBinary `env:"V"`
	}
	out := encodeToString(t, cfg{V: encodeBinary{v: "x"}})
	if !strings.Contains(out, "V=binary:x") {
		t.Errorf("got %q", out)
	}
}

// --- Marshaler / TextMarshaler / BinaryMarshaler error propagation ---

type marshalerError struct{}

func (*marshalerError) MarshalEnv() (string, error) { return "", errors.New("env-fail") }

func TestEncode_MarshalerError(t *testing.T) {
	type cfg struct {
		V marshalerError `env:"V"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{})
	if err == nil || !strings.Contains(err.Error(), "env-fail") {
		t.Errorf("err = %v", err)
	}
}

type textMarshalerError struct{}

func (*textMarshalerError) MarshalText() ([]byte, error) { return nil, errors.New("text-fail") }

func TestEncode_TextMarshalerError(t *testing.T) {
	type cfg struct {
		V textMarshalerError `env:"V"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{})
	if err == nil || !strings.Contains(err.Error(), "text-fail") {
		t.Errorf("err = %v", err)
	}
}

type binaryMarshalerError struct{}

func (*binaryMarshalerError) MarshalBinary() ([]byte, error) { return nil, errors.New("binary-fail") }

func TestEncode_BinaryMarshalerError(t *testing.T) {
	type cfg struct {
		V binaryMarshalerError `env:"V"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{})
	if err == nil || !strings.Contains(err.Error(), "binary-fail") {
		t.Errorf("err = %v", err)
	}
}

// --- WithCustomMarshaler ---

func TestEncode_CustomMarshaler(t *testing.T) {
	type myType string
	type cfg struct {
		V myType `env:"V"`
	}
	out := encodeToString(t, cfg{V: "x"},
		WithCustomMarshaler(func(v myType) (string, error) {
			return "custom:" + string(v), nil
		}),
	)
	if !strings.Contains(out, "V=custom:x") {
		t.Errorf("got %q", out)
	}
}

func TestEncode_CustomMarshalerError(t *testing.T) {
	type myType string
	type cfg struct {
		V myType `env:"V"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{V: "x"},
		WithCustomMarshaler(func(myType) (string, error) {
			return "", errors.New("custom-fail")
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "custom-fail") {
		t.Errorf("err = %v", err)
	}
}

func TestEncode_CustomMarshaler_Wins(t *testing.T) {
	// Custom marshaler runs at the top of the chain, ahead of Marshaler /
	// TextMarshaler / BinaryMarshaler.
	type cfg struct {
		V encodeMyValue `env:"V"`
	}
	out := encodeToString(t, cfg{V: encodeMyValue{via: "x"}},
		WithCustomMarshaler(func(encodeMyValue) (string, error) {
			return "custom-wins", nil
		}),
	)
	if !strings.Contains(out, "V=custom-wins") {
		t.Errorf("custom marshaler did not override Marshaler interface: %q", out)
	}
}

// --- unsupported type ---

func TestEncode_UnsupportedType(t *testing.T) {
	type cfg struct {
		C chan int `env:"C"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{C: make(chan int)})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

// --- EncodeFile ---

func TestEncodeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.env")

	type cfg struct {
		Port int `env:"PORT"`
	}
	if err := EncodeFile(path, cfg{Port: 9000}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "PORT=9000") {
		t.Errorf("file content: %q", data)
	}
	assertOwnerOnly(t, path)
}

func TestEncodeFile_OpenFails(t *testing.T) {
	// Try to open a path inside a non-existent directory.
	err := EncodeFile("/nonexistent/dir/out.env", encodeBasicConfig{})
	if err == nil {
		t.Fatal("expected error opening file in non-existent dir")
	}
}

func TestEncodeFile_TruncatesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.env")
	if err := os.WriteFile(path, []byte("OLD=content\n# comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type cfg struct {
		New int `env:"NEW"`
	}
	if err := EncodeFile(path, cfg{New: 1}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "OLD=") {
		t.Errorf("file not truncated:\n%s", data)
	}
	if !strings.Contains(string(data), "NEW=1") {
		t.Errorf("new content missing:\n%s", data)
	}
}

// --- loader-aware Encode / EncodeFile ---

func TestLoader_Encode_HonorsPrefix(t *testing.T) {
	type cfg struct {
		Port int    `env:"PORT,default=8080"`
		Name string `env:"NAME"`
	}
	l := New(WithPrefix("APP_"))
	var buf bytes.Buffer
	if err := l.Encode(&buf, cfg{Port: 9000, Name: "svc"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := "APP_PORT=9000\nAPP_NAME=svc\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	// Package-level Encode has no prefix.
	pkg := encodeToString(t, cfg{Port: 9000, Name: "svc"})
	if strings.Contains(pkg, "APP_") {
		t.Errorf("package-level Encode added a prefix: %q", pkg)
	}
}

func TestLoader_Encode_HonorsTagName(t *testing.T) {
	type cfg struct {
		Port int `cfg:"DB_PORT"`
	}
	l := New(WithTagName("cfg"))
	var buf bytes.Buffer
	if err := l.Encode(&buf, cfg{Port: 5432}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := "DB_PORT=5432\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	// Package-level Encode reads the "env" tag, so the cfg tag is invisible
	// and the key falls back to the SCREAMING_SNAKE_CASE field name.
	if pkg := encodeToString(t, cfg{Port: 5432}); !strings.Contains(pkg, "PORT=5432") || strings.Contains(pkg, "DB_PORT") {
		t.Errorf("package-level Encode should use the field name, got %q", pkg)
	}
}

func TestLoader_Encode_HonorsFieldNameTransform(t *testing.T) {
	type cfg struct {
		Port int    // untagged → key comes from the transform
		Name string // untagged
	}
	l := New(WithFieldNameTransform(func(s string) string { return "X_" + strings.ToUpper(s) }))
	var buf bytes.Buffer
	if err := l.Encode(&buf, cfg{Port: 1, Name: "n"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := "X_PORT=1\nX_NAME=n\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestLoader_Encode_CommentsKeyedByPrefixedName(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT"`
	}
	l := New(WithPrefix("APP_"))
	out := func(comments map[string]string) string {
		var buf bytes.Buffer
		if err := l.Encode(&buf, cfg{Port: 1}, WithEncodeComments(comments)); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		return buf.String()
	}
	if got := out(map[string]string{"APP_PORT": "the port"}); !strings.Contains(got, "# the port\n") {
		t.Errorf("comment keyed by prefixed name not applied: %q", got)
	}
	if got := out(map[string]string{"PORT": "unprefixed"}); strings.Contains(got, "unprefixed") {
		t.Errorf("comment keyed by unprefixed name should not match: %q", got)
	}
}

func TestLoader_Encode_RoundTripsThroughLoad(t *testing.T) {
	type cfg struct {
		Port  int    `env:"PORT"`
		Token string `env:"TOKEN,secret"`
	}
	l := New(WithPrefix("SVC_"))
	original := cfg{Port: 7000, Token: "hunter2"}
	var buf bytes.Buffer
	if err := l.Encode(&buf, original, WithEncodeIncludeSecrets(true)); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	parsed := parseEncodedToMap(t, buf.String())
	back := New(WithPrefix("SVC_"), WithSource(Map(parsed)))
	var got cfg
	if err := back.Load(&got); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, original)
	}
}

func TestLoader_EncodeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.env")
	type cfg struct {
		Port int `env:"PORT"`
	}
	l := New(WithPrefix("APP_"))
	if err := l.EncodeFile(path, cfg{Port: 9000}); err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "APP_PORT=9000") {
		t.Errorf("file content: %q", data)
	}
	assertOwnerOnly(t, path)
}

func TestLoader_Encode_NotStruct(t *testing.T) {
	var buf bytes.Buffer
	if err := New().Encode(&buf, 42); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
	if err := New().EncodeFile(filepath.Join(t.TempDir(), "x"), nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestLoader_Encode_StructInfoError(t *testing.T) {
	type cfg struct {
		A string `cfg:"FOO"`
		B string `cfg:"FOO"` // conflict
	}
	var buf bytes.Buffer
	if err := New(WithTagName("cfg")).Encode(&buf, cfg{}); !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", err)
	}
}

// --- round-trip stability ---

type roundTripConfig struct {
	Port    int               `env:"PORT,default=8080"`
	DBURL   string            `env:"DATABASE_URL"`
	Timeout time.Duration     `env:"TIMEOUT"`
	Hosts   []string          `env:"HOSTS"`
	Tags    map[string]string `env:"TAGS"`
	Stamp   time.Time         `env:"STAMP"`
}

func TestEncode_RoundTrip(t *testing.T) {
	original := roundTripConfig{
		Port:    9000,
		DBURL:   "postgres://localhost/db",
		Timeout: 45 * time.Second,
		Hosts:   []string{"a", "b", "c"},
		Tags:    map[string]string{"env": "prod", "tier": "frontend"},
		Stamp:   time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
	}

	var buf bytes.Buffer
	if err := Encode(&buf, original); err != nil {
		t.Fatal(err)
	}

	// Parse the encoded output back into a Map and decode.
	parsed := parseEncodedToMap(t, buf.String())
	loader := New(WithSource(Map(parsed)))
	var got roundTripConfig
	if err := loader.Load(&got); err != nil {
		t.Fatalf("re-decode failed: %v", err)
	}

	if got.Port != original.Port ||
		got.DBURL != original.DBURL ||
		got.Timeout != original.Timeout ||
		!reflect.DeepEqual(got.Hosts, original.Hosts) ||
		!reflect.DeepEqual(got.Tags, original.Tags) ||
		!got.Stamp.Equal(original.Stamp) {
		t.Errorf("round-trip mismatch:\noriginal: %+v\ngot:      %+v\nencoded:\n%s",
			original, got, buf.String())
	}
}

func TestEncode_RoundTrip_QuotedValues(t *testing.T) {
	type cfg struct {
		WithSpaces  string `env:"WITH_SPACES"`
		WithQuotes  string `env:"WITH_QUOTES"`
		WithDollar  string `env:"WITH_DOLLAR"`
		WithNewline string `env:"WITH_NEWLINE"`
	}
	original := cfg{
		WithSpaces:  "value with spaces",
		WithQuotes:  `she said "hello"`,
		WithDollar:  "$HOME/cache",
		WithNewline: "line1\nline2",
	}

	var buf bytes.Buffer
	if err := Encode(&buf, original); err != nil {
		t.Fatal(err)
	}

	parsed := parseEncodedToMap(t, buf.String())
	loader := New(WithSource(Map(parsed)))
	var got cfg
	if err := loader.Load(&got); err != nil {
		t.Fatalf("re-decode failed: %v", err)
	}
	if got != original {
		t.Errorf("round-trip mismatch:\noriginal: %+v\ngot:      %+v", original, got)
	}
}

// parseEncodedToMap is a minimal parser exercising the encoder's
// output shape: bare values, double-quoted values with the standard
// escape table, comments, and `export ` prefixes. The full parser
// lives in github.com/go-rotini/dotenv.
func parseEncodedToMap(t *testing.T, src string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
			value = unquoteTestValue(value[1 : len(value)-1])
		}
		out[key] = value
	}
	return out
}

func unquoteTestValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '$':
			b.WriteByte('$')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// --- needsQuoting / quoteValue / formatValue helpers ---

func TestNeedsQuoting(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"plain", false},
		{"plain123", false},
		{"with space", true},
		{"with#hash", true},
		{"with$dollar", true},
		{`has"quote`, true},
		{`has'apos`, true},
		{`has\backslash`, true},
		{"has`backtick", true},
		{"has\ttab", true},
		{"has\nnewline", true},
		{"has\x01ctrl", true},
		{"has\x7fdelete", true},
	}
	for _, c := range cases {
		if got := needsQuoting(c.in); got != c.want {
			t.Errorf("needsQuoting(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatValue(t *testing.T) {
	if got := formatValue("plain", false); got != "plain" {
		t.Errorf("got %q", got)
	}
	if got := formatValue("plain", true); got != `"plain"` {
		t.Errorf("quoteAll: got %q", got)
	}
	if got := formatValue("with space", false); got != `"with space"` {
		t.Errorf("got %q", got)
	}
}

// Sanity for completeness: confirm the encoder writes lines correctly for
// a struct with every supported tag option simultaneously.
func TestEncode_KitchenSink(t *testing.T) {
	type cfg struct {
		Port    int               `env:"PORT,default=8080"           envDesc:"HTTP port"`
		Token   string            `env:"TOKEN,required,secret"       envDesc:"API token (redacted)"`
		Hosts   []string          `env:"HOSTS,separator=;"`
		Tags    map[string]string `env:"TAGS,separator=|,kvSeparator=="`
		Created time.Time         `env:"CREATED,layout=2006-01-02"`
		Bytes   []byte            `env:"BYTES,base64"`
	}
	c := cfg{
		Port:    8080,
		Token:   "supersecret",
		Hosts:   []string{"a", "b", "c"},
		Tags:    map[string]string{"env": "prod", "tier": "fe"},
		Created: time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
		Bytes:   []byte("hello"),
	}
	out := encodeToString(t, c, WithEncodeExport(true))

	checks := []string{
		"# HTTP port",
		"export PORT=8080",
		"# API token (redacted)",
		"export TOKEN=***",
		"export HOSTS=a;b;c",
		"env=prod|tier=fe", // map ordering is sorted
		"export CREATED=2026-05-08",
		"export BYTES=aGVsbG8=",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	// Original token must NOT appear.
	if strings.Contains(out, "supersecret") {
		t.Errorf("secret leaked:\n%s", out)
	}
}

// --- formatValue with all-quoting and various inputs ---

func TestEncode_QuoteAllForcesQuoting(t *testing.T) {
	type cfg struct {
		X int    `env:"X"`
		Y string `env:"Y"`
	}
	out := encodeToString(t, cfg{X: 1, Y: ""}, WithEncodeQuoteAll(true))
	if !strings.Contains(out, `X="1"`) || !strings.Contains(out, `Y=""`) {
		t.Errorf("quoteAll: %q", out)
	}
}

// Control bytes outside the escape table round-trip as their literal
// form within the double-quoted output.
func TestQuoteValueRendersControlChars(t *testing.T) {
	got := quoteValue("\x01\x02\x03")
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("got %q", got)
	}
	_ = fmt.Sprintf("%q", got)
}

// --- failing writer ---

// failingWriter returns an error after a configurable byte budget.
// allowance==0 fails on the first write; >0 lets that many bytes through
// before failing.
type failingWriter struct {
	allowance int
}

var errFailingWriterTest = errors.New("failing writer test")

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.allowance <= 0 {
		return 0, errFailingWriterTest
	}
	if len(p) <= w.allowance {
		w.allowance -= len(p)
		return len(p), nil
	}
	n := w.allowance
	w.allowance = 0
	return n, errFailingWriterTest
}

func TestEncode_WriteError_OnFlush(t *testing.T) {
	// The bufio.Writer's default 4096-byte buffer holds small outputs
	// without flushing until Flush() at the end. The error surfaces from
	// Flush, hitting the wrapped-error branch.
	type cfg struct {
		X int `env:"X"`
	}
	w := &failingWriter{}
	err := Encode(w, cfg{X: 1})
	if err == nil {
		t.Fatal("expected error from failing writer")
	}
	if !strings.Contains(err.Error(), "flush") {
		t.Errorf("want flush error, got %v", err)
	}
}

func TestEncode_WriteError_OnLongValue(t *testing.T) {
	// A value longer than bufio.Writer's default 4096-byte buffer forces
	// a Flush to the underlying writer mid-WriteString, exercising the
	// value-write error branch.
	type cfg struct {
		Long string `env:"LONG"`
	}
	c := cfg{Long: strings.Repeat("x", 5000)}
	err := Encode(&failingWriter{}, c)
	if err == nil {
		t.Fatal("expected error from failing writer on long value")
	}
}

func TestEncodeFile_EncodeError(t *testing.T) {
	// EncodeFile delegates to Encode after opening the file. A struct
	// with conflicting fields produces an Encode-time error that
	// EncodeFile passes through.
	type cfg struct {
		A string `env:"X"`
		B string `env:"X"`
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.env")
	err := EncodeFile(path, cfg{})
	if !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", err)
	}
}

func TestEncode_WriteError_OnExport(t *testing.T) {
	// Combine a description that exactly fills the bufio buffer with
	// WithEncodeExport so the next "export " write overflows.
	desc := strings.Repeat("a", 4093) // "# " + 4093 + "\n" = exactly 4096
	type cfg struct {
		X int `env:"X"`
	}
	c := cfg{X: 1}
	w := &failingWriter{allowance: 4095} // permit comment flush, fail export
	err := Encode(w, c,
		WithEncodeComments(map[string]string{"X": desc}),
		WithEncodeExport(true),
	)
	if err == nil {
		t.Fatal("expected error from failing writer on export prefix")
	}
}

func TestEncode_WriteError_OnComment(t *testing.T) {
	// A small bufio buffer would avoid this; we can force Fprintf to
	// fail by giving the underlying writer zero allowance and writing a
	// description line. The bufio.Writer attempts to flush its internal
	// buffer when the description-line write spills it.
	//
	// To reliably hit the comment-write error path we wrap the writer in
	// a NoBuffer wrapper that bypasses bufio. But Encode hardcodes
	// bufio.NewWriter; we can't bypass.
	//
	// Instead, construct a struct whose envDesc plus subsequent writes
	// exceed 4096 bytes — making Fprintf trigger an internal Flush
	// during description emission.
	long := strings.Repeat("x", 5000)
	type cfg struct {
		X int `env:"X" envDesc:""`
	}
	c := cfg{X: 1}
	w := &failingWriter{}
	err := Encode(w, c, WithEncodeComments(map[string]string{"X": long}))
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- element-render errors ---

func TestEncode_SliceElementError(t *testing.T) {
	// Channels are unsupported; encoding a slice of channels should
	// surface the element error.
	type cfg struct {
		Cs []chan int `env:"CS"`
	}
	c := cfg{Cs: []chan int{nil}}
	var buf bytes.Buffer
	err := Encode(&buf, c)
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestEncode_MapValueError(t *testing.T) {
	type cfg struct {
		M map[string]chan int `env:"M"`
	}
	m := map[string]chan int{"a": nil}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{M: m})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestEncode_MapKeyError(t *testing.T) {
	// Use a complex64 key: not in Go's allowed comparable set... actually
	// complex IS comparable, so map[complex64]string is legal Go. The
	// renderer (decodeReflect's complex case) returns ErrUnsupportedType.
	type cfg struct {
		M map[complex64]string `env:"M"`
	}
	c := cfg{M: map[complex64]string{1 + 2i: "x"}}
	var buf bytes.Buffer
	err := Encode(&buf, c)
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestEncode_SecretWrapperRedactedWithoutTag(t *testing.T) {
	type cfg struct {
		Token  Secret[string]  `env:"TOKEN"`
		PToken *Secret[string] `env:"P_TOKEN"`
	}
	p := NewSecret("hunter3")
	v := cfg{Token: NewSecret("hunter2"), PToken: &p}
	var buf bytes.Buffer
	if err := Encode(&buf, v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "hunter") {
		t.Fatalf("Encode leaked a Secret value:\n%s", out)
	}
	if !strings.Contains(out, "TOKEN=***") || !strings.Contains(out, "P_TOKEN=***") {
		t.Fatalf("want redacted lines:\n%s", out)
	}
	buf.Reset()
	if err := Encode(&buf, v, WithEncodeIncludeSecrets(true)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "TOKEN=hunter2") {
		t.Fatalf("WithEncodeIncludeSecrets lost the value:\n%s", buf.String())
	}
}

func TestEncode_SecretContainersRedacted(t *testing.T) {
	t.Parallel()
	c := struct {
		Tokens []Secret[string]          `env:"TOKENS"`
		ByHost map[string]Secret[string] `env:"BY_HOST"`
	}{
		Tokens: []Secret[string]{NewSecret("s3cretA")},
		ByHost: map[string]Secret[string]{"a": NewSecret("s3cretC")},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, &c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "s3cret") {
		t.Errorf("secret leaked:\n%s", buf.String())
	}
	for _, d := range Describe(&c) {
		if !d.Secret {
			t.Errorf("%s not marked secret", d.Name)
		}
	}
}

func TestEncode_SeparatorInElement(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]any{
		"slice": &struct{ L []string }{L: []string{"a,b", "c"}},
		"custom sep": &struct {
			L []string `env:"L,separator=;"`
		}{L: []string{"a;b"}},
		"map key":   &struct{ M map[string]string }{M: map[string]string{"k:1": "v"}},
		"map value": &struct{ M map[string]string }{M: map[string]string{"k": "v,w"}},
	} {
		if err := Encode(io.Discard, v); !errors.Is(err, ErrSeparatorInValue) {
			t.Errorf("%s: Encode = %v, want ErrSeparatorInValue", name, err)
		}
	}
	ok := &struct{ M map[string]string }{M: map[string]string{"k": "v:w"}}
	if err := Encode(io.Discard, ok); err != nil {
		t.Errorf("kv separator in a map value loads back fine: %v", err)
	}
}

func TestEncode_SingleEmptyArrayElementRejected(t *testing.T) {
	t.Parallel()
	type cfg struct {
		A [1]string `env:"A"`
	}
	if err := Encode(io.Discard, cfg{A: [1]string{""}}); !errors.Is(err, ErrSeparatorInValue) {
		t.Errorf("Encode([1]string{\"\"}) = %v, want ErrSeparatorInValue", err)
	}
}

func TestEncode_SecretSkipsCustomMarshalerForValue(t *testing.T) {
	t.Parallel()
	type cfg struct {
		S Secret[string] `env:"S"`
		P string         `env:"P"`
	}
	var buf bytes.Buffer
	err := Encode(&buf, cfg{S: NewSecret("ab"), P: "ab"}, WithEncodeIncludeSecrets(true),
		WithCustomMarshaler(func(s string) (string, error) { return strings.ToUpper(s), nil }))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "S=ab\nP=AB\n"; got != want {
		t.Errorf("Encode = %q, want %q", got, want)
	}
}

func TestEncode_SingleEmptyElementRejected(t *testing.T) {
	t.Parallel()
	type cfg struct {
		L []string `env:"L"`
	}
	if err := Encode(io.Discard, cfg{L: []string{""}}); !errors.Is(err, ErrSeparatorInValue) {
		t.Errorf("Encode([]string{\"\"}) = %v, want ErrSeparatorInValue", err)
	}
	for _, l := range [][]string{{}, {"", ""}, {"a"}} {
		var buf bytes.Buffer
		if err := Encode(&buf, cfg{L: l}); err != nil {
			t.Errorf("Encode(%q) = %v", l, err)
			continue
		}
		var out cfg
		val := strings.TrimSuffix(strings.TrimPrefix(buf.String(), "L="), "\n")
		if err := New(WithSource(Map(map[string]string{"L": val}))).Load(&out); err != nil || !slices.Equal(out.L, l) {
			t.Errorf("%q round trip = %q, %v", l, out.L, err)
		}
	}
}
