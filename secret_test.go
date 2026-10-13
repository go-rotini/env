package env

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- Secret[T] basics ---

func TestSecret_Value(t *testing.T) {
	s := NewSecret("the-token")
	if s.Value() != "the-token" {
		t.Errorf("Value() = %q", s.Value())
	}
}

func TestSecret_String(t *testing.T) {
	s := NewSecret("the-token")
	if s.String() != "<redacted>" {
		t.Errorf("String() = %q", s.String())
	}
}

func TestSecret_GoString(t *testing.T) {
	s := NewSecret("the-token")
	if s.GoString() != "<redacted>" {
		t.Errorf("GoString() = %q", s.GoString())
	}
}

func TestSecret_LogValue(t *testing.T) {
	s := NewSecret("the-token")
	v := s.LogValue()
	if v.String() != "<redacted>" {
		t.Errorf("LogValue() = %q", v.String())
	}
}

func TestSecret_FmtFormatting(t *testing.T) {
	s := NewSecret("the-token")
	cases := map[string]string{
		"%v":  "<redacted>",
		"%s":  "<redacted>",
		"%#v": "<redacted>",
	}
	for verb, want := range cases {
		got := fmt.Sprintf(verb, s)
		if got != want {
			t.Errorf("Sprintf(%q, secret) = %q, want %q", verb, got, want)
		}
	}
	// %q wraps the String() result in double quotes.
	if got := fmt.Sprintf("%q", s); got != `"<redacted>"` {
		t.Errorf("Sprintf(%%q, secret) = %q", got)
	}
}

func TestSecret_MarshalJSON(t *testing.T) {
	s := NewSecret("the-token")
	// MarshalJSON returns the raw bytes; the marker is unescaped so the
	// caller can decide how to handle HTML-special characters.
	got, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"<redacted>"` {
		t.Errorf("MarshalJSON = %s", got)
	}

	// json.Marshal post-processes MarshalJSON output by HTML-escaping
	// `<` and `>` (this is encoding/json's default behavior, not ours).
	// Verify the integration nonetheless: the secret value never leaks,
	// and the redaction marker is preserved (just escape-encoded).
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got2 := string(out)
	if strings.Contains(got2, "the-token") {
		t.Errorf("real secret leaked through json.Marshal: %s", got2)
	}
	if !strings.Contains(got2, "redacted") {
		t.Errorf("redaction marker missing from json.Marshal: %s", got2)
	}
}

// --- slog integration ---

func TestSecret_SlogRedacts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	s := NewSecret("the-real-secret")
	logger.Info("loaded", "token", s)
	out := buf.String()
	if strings.Contains(out, "the-real-secret") {
		t.Errorf("real secret leaked into log:\n%s", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Errorf("redaction marker missing:\n%s", out)
	}
}

// --- Secret[T] generic instantiations ---

func TestSecret_Int(t *testing.T) {
	s := NewSecret(42)
	if s.Value() != 42 {
		t.Errorf("Value = %d", s.Value())
	}
	if s.String() != "<redacted>" {
		t.Errorf("String = %q", s.String())
	}
}

func TestSecret_Bool(t *testing.T) {
	s := NewSecret(true)
	if !s.Value() {
		t.Error("Value should be true")
	}
	if s.String() != "<redacted>" {
		t.Errorf("String = %q", s.String())
	}
}

// --- decoder integration: UnmarshalEnv ---

func TestSecret_UnmarshalEnv_String(t *testing.T) {
	type cfg struct {
		Token Secret[string] `env:"TOKEN"`
	}
	loader := New(WithSource(Map(map[string]string{"TOKEN": "real-token"})))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Token.Value() != "real-token" {
		t.Errorf("Token = %q", c.Token.Value())
	}
	// Printing must redact.
	if got := fmt.Sprint(c.Token); got != "<redacted>" {
		t.Errorf("got %q", got)
	}
}

func TestSecret_UnmarshalEnv_Int(t *testing.T) {
	type cfg struct {
		Pin Secret[int] `env:"PIN"`
	}
	loader := New(WithSource(Map(map[string]string{"PIN": "1234"})))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Pin.Value() != 1234 {
		t.Errorf("Pin = %d", c.Pin.Value())
	}
}

func TestSecret_UnmarshalEnv_ParseError(t *testing.T) {
	type cfg struct {
		Pin Secret[int] `env:"PIN"`
	}
	loader := New(WithSource(Map(map[string]string{"PIN": "not-a-number"})))
	var c cfg
	err := loader.Load(&c)
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- encoder integration: Secret without the secret tag ---

type secretWrapperConfig struct {
	Token Secret[string] `env:"TOKEN"`
}

func TestSecret_Encode_WithoutSecretTag(t *testing.T) {
	// A Secret field is redacted even without the secret tag;
	// WithEncodeIncludeSecrets writes the MarshalEnv value.
	c := secretWrapperConfig{Token: NewSecret("the-real-token")}
	var buf bytes.Buffer
	if err := Encode(&buf, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "TOKEN=***") {
		t.Errorf("expected redaction:\n%s", buf.String())
	}
	buf.Reset()
	if err := Encode(&buf, c, WithEncodeIncludeSecrets(true)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "TOKEN=the-real-token") {
		t.Errorf("expected MarshalEnv to expose value:\n%s", buf.String())
	}
}

type secretTaggedWrapperConfig struct {
	Token Secret[string] `env:"TOKEN,secret"`
}

func TestSecret_Encode_WithSecretTagRedacts(t *testing.T) {
	// With the secret tag, the encoder redacts to "***" before MarshalEnv
	// is called.
	c := secretTaggedWrapperConfig{Token: NewSecret("the-real-token")}
	var buf bytes.Buffer
	if err := Encode(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "the-real-token") {
		t.Errorf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "TOKEN=***") {
		t.Errorf("not redacted: %s", out)
	}
}

func TestSecret_Encode_IncludeSecretsViaWrapper(t *testing.T) {
	// With secret tag AND WithEncodeIncludeSecrets, MarshalEnv runs and
	// returns the inner value.
	c := secretTaggedWrapperConfig{Token: NewSecret("the-real-token")}
	var buf bytes.Buffer
	if err := Encode(&buf, c, WithEncodeIncludeSecrets(true)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "TOKEN=the-real-token") {
		t.Errorf("WithEncodeIncludeSecrets did not expose Secret[T] inner value:\n%s", buf.String())
	}
}

// --- WithSecretRedactor for ParseError ---

type secretParseConfig struct {
	APIKey int `env:"API_KEY,secret"`
}

func TestSecret_ParseErrorRedacts(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"API_KEY": "not-a-number"})))
	var c secretParseConfig
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	// The actual value "not-a-number" must NOT appear in the error message.
	if strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("secret value leaked into ParseError: %v", err)
	}
	// Default redactor's marker should appear.
	if !strings.Contains(err.Error(), "<redacted") {
		t.Errorf("expected redaction marker in error: %v", err)
	}
}

func TestSecret_CustomRedactor(t *testing.T) {
	loader := New(
		WithSource(Map(map[string]string{"API_KEY": "not-a-number"})),
		WithSecretRedactor(func(string) string {
			return "[hidden]"
		}),
	)
	var c secretParseConfig
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "[hidden]") {
		t.Errorf("custom redactor not applied: %v", err)
	}
	if strings.Contains(err.Error(), "<redacted") {
		t.Errorf("default redactor also fired: %v", err)
	}
}

func TestSecret_CustomRedactor_NilRestoresDefault(t *testing.T) {
	loader := New(
		WithSource(Map(map[string]string{"API_KEY": "x"})),
		WithSecretRedactor(func(string) string { return "[hidden]" }),
		WithSecretRedactor(nil), // restore default
	)
	var c secretParseConfig
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "[hidden]") {
		t.Errorf("nil should have restored default; got: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted") {
		t.Errorf("default redactor not active: %v", err)
	}
}

func TestDefaultSecretRedactor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "<redacted>"},
		{"x", "<redacted: 1 byte>"},
		{"abcdef", "<redacted: 6 bytes>"},
	}
	for _, tc := range cases {
		if got := defaultSecretRedactor(tc.in); got != tc.want {
			t.Errorf("defaultSecretRedactor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- secret tag does not redact non-error happy path values ---

type secretHappyConfig struct {
	APIKey string `env:"API_KEY,secret"`
}

// loader.redactor() falls back to defaultSecretRedactor when the loader's
// option is nil. New() always installs a default, so a direct unit test
// of the fallback is needed.
func TestLoader_RedactorFallback(t *testing.T) {
	l := &Loader{opts: loaderOptions{}} // no defaults applied
	r := l.redactor()
	if got := r("abc"); got != "<redacted: 3 bytes>" {
		t.Errorf("redactor fallback = %q", got)
	}
}

func TestSecret_HappyPathLeavesValueIntact(t *testing.T) {
	// When the secret-tagged field decodes successfully, the field value
	// itself is the real secret. Redaction only affects error messages
	// and observability surfaces. The user can read field.Value freely
	// (the loader is not in the business of replacing user values).
	loader := New(WithSource(Map(map[string]string{"API_KEY": "real-secret"})))
	var c secretHappyConfig
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "real-secret" {
		t.Errorf("APIKey = %q (loader should not redact the field value)", c.APIKey)
	}
}

func TestSecret_FormatAllVerbs(t *testing.T) {
	type cfg struct {
		Port  Secret[int]
		Token Secret[string]
		On    Secret[bool]
	}
	c := cfg{NewSecret(31337), NewSecret("hunter2"), NewSecret(true)}
	for _, verb := range []string{"%d", "%t", "%o", "%b", "%e", "%f", "%10d", "%x", "%X", "%+v", "%#v", "%v", "%s", "%q"} {
		for _, v := range []any{c.Port, c.Token, c.On, c} {
			got := fmt.Sprintf(verb, v)
			if strings.Contains(got, "31337") || strings.Contains(got, "hunter2") || strings.Contains(got, "true") {
				t.Errorf("Sprintf(%q, %T) = %q leaks the value", verb, v, got)
			}
		}
	}
	if got := fmt.Sprintf("%12d|%-12v|", c.Port, c.Port); got != "  <redacted>|<redacted>  |" {
		t.Errorf("width = %q", got)
	}
}

// secretVerbs holds the verbs fmt formats by reflection for a Secret
// without calling Format; a variable keeps vet from flagging %w.
var secretVerbs = []string{"%p", "%w"}

// TestSecret_RejectedVerbsPrintTheValue records the documented limit:
// %p and %w print the value, because fmt formats them by reflection
// without calling Format. go vet reports %w but not %p, since Secret
// implements fmt.Formatter.
func TestSecret_RejectedVerbsPrintTheValue(t *testing.T) {
	s := NewSecret("hunter2")
	for _, verb := range secretVerbs {
		if got := fmt.Sprintf(verb, s); !strings.Contains(got, "%!") {
			t.Errorf("%q = %q; fmt no longer rejects it, so update the Secret godoc", verb, got)
		}
	}
}

func TestSecret_EqualityComparesValues(t *testing.T) {
	type cfg struct {
		Token Secret[string]
		Port  int
	}
	if NewSecret("a") != NewSecret("a") || NewSecret("a") == NewSecret("b") {
		t.Error("== on Secrets does not compare values")
	}
	var loaded cfg
	if err := loaded.Token.UnmarshalEnv("a"); err != nil {
		t.Fatal(err)
	}
	if loaded != (cfg{Token: NewSecret("a")}) {
		t.Error("configs holding equal Secrets compare unequal")
	}
}

func TestSecret_TagOptionsApplied(t *testing.T) {
	type cfg struct {
		Key  Secret[[]byte]    `env:"KEY,base64"`
		Hex  Secret[[]byte]    `env:"HEX,hex"`
		List Secret[[]string]  `env:"LIST,separator=;"`
		Day  Secret[time.Time] `env:"DAY,layout=2006-01-02"`
	}
	src := Map(map[string]string{"KEY": "aGVsbG8=", "HEX": "6869", "LIST": "a;b", "DAY": "2026-10-12"})
	var c cfg
	if err := New(WithSource(src)).Load(&c); err != nil {
		t.Fatal(err)
	}
	if string(c.Key.Value()) != "hello" || string(c.Hex.Value()) != "hi" ||
		!slices.Equal(c.List.Value(), []string{"a", "b"}) ||
		!c.Day.Value().Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("loaded %q %q %q %v", c.Key.Value(), c.Hex.Value(), c.List.Value(), c.Day.Value())
	}
	out := encodeToString(t, c, WithEncodeIncludeSecrets(true), WithEncodeSort(true))
	want := "DAY=2026-10-12\nHEX=6869\nKEY=aGVsbG8=\nLIST=a;b\n"
	if out != want {
		t.Errorf("Encode = %q, want %q", out, want)
	}
}

func TestSecret_UntaggedFieldLoads(t *testing.T) {
	type cfg struct {
		Token Secret[string]
		Plain string
	}
	var c cfg
	l := New(WithSource(Map(map[string]string{"TOKEN": "x", "PLAIN": "y"})))
	if err := l.Load(&c); err != nil || c.Token.Value() != "x" {
		t.Fatalf("Load = %v, Token = %q; want x", err, c.Token.Value())
	}
	docs := l.Describe(&c)
	if len(docs) != 2 || docs[0].Name != "TOKEN" || !docs[0].Secret {
		t.Errorf("Describe = %+v, want a secret TOKEN row", docs)
	}
}

func TestLoader_SecretAssertionMessageDropped(t *testing.T) {
	type cfg struct {
		E string `env:"E,secret,expand"`
		P string `env:"P,expand"`
	}
	src := Map(map[string]string{"E": "${MISSING:?no $TOK}", "P": "${MISSING:?no $TOK}", "TOK": "s3cretE"})
	err := New(WithSource(src)).Load(&cfg{})
	if !errors.Is(err, ErrExpansionAssertion) {
		t.Fatalf("Load = %v, want ErrExpansionAssertion", err)
	}
	var secretErr, plainErr string
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		switch {
		case strings.Contains(e.Error(), "field E:"):
			secretErr = fmt.Sprintf("%+v", e)
		case strings.Contains(e.Error(), "field P:"):
			plainErr = e.Error()
		}
	}
	if secretErr == "" || strings.Contains(secretErr, "s3cretE") || !strings.Contains(secretErr, `"MISSING"`) {
		t.Errorf("secret field error = %q", secretErr)
	}
	if !strings.Contains(plainErr, "no s3cretE") {
		t.Errorf("plain field error = %q, want the expanded message", plainErr)
	}
}
