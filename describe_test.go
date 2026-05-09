package env

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- Describe basics ---

type describeBasicConfig struct {
	Port    int           `env:"PORT,default=8080" envDesc:"HTTP listen port"`
	DBURL   string        `env:"DATABASE_URL,required" envDesc:"Primary database DSN"`
	APIKey  string        `env:"API_KEY,required,secret" envDesc:"Upstream API credential"`
	Timeout time.Duration `env:"TIMEOUT,default=30s" envDesc:"Request timeout"`
	Hosts   []string      `env:"HOSTS,separator=;" envDesc:"Allowed host list"`
}

func TestDescribe_PopulatesFields(t *testing.T) {
	docs := Describe(describeBasicConfig{})
	if len(docs) != 5 {
		t.Fatalf("got %d docs, want 5", len(docs))
	}

	port := docs[0]
	if port.Name != "PORT" {
		t.Errorf("Name = %q", port.Name)
	}
	if port.GoType != "int" {
		t.Errorf("GoType = %q", port.GoType)
	}
	if port.Default != "8080" {
		t.Errorf("Default = %q", port.Default)
	}
	if port.Description != "HTTP listen port" {
		t.Errorf("Description = %q", port.Description)
	}
	if port.Required {
		t.Error("Port should not be required")
	}
	if port.Secret {
		t.Error("Port should not be secret")
	}

	db := docs[1]
	if !db.Required {
		t.Error("DBURL should be required")
	}

	api := docs[2]
	if !api.Required || !api.Secret {
		t.Errorf("APIKey: required=%v secret=%v", api.Required, api.Secret)
	}

	timeout := docs[3]
	if timeout.GoType != "time.Duration" {
		t.Errorf("Timeout GoType = %q", timeout.GoType)
	}

	hosts := docs[4]
	if hosts.GoType != "[]string" {
		t.Errorf("Hosts GoType = %q", hosts.GoType)
	}
	if hosts.Separator != ";" {
		t.Errorf("Hosts separator = %q", hosts.Separator)
	}
}

func TestDescribe_AcceptsPointer(t *testing.T) {
	docs := Describe(&describeBasicConfig{})
	if len(docs) != 5 {
		t.Errorf("pointer form did not produce docs")
	}
}

func TestDescribe_NilReturnsNil(t *testing.T) {
	if got := Describe(nil); got != nil {
		t.Errorf("Describe(nil) = %v, want nil", got)
	}
	var p *describeBasicConfig
	if got := Describe(p); got != nil {
		t.Errorf("Describe(nil-pointer) = %v, want nil", got)
	}
}

func TestDescribe_NotStructReturnsNil(t *testing.T) {
	var x int
	if got := Describe(&x); got != nil {
		t.Errorf("Describe(*int) = %v, want nil", got)
	}
	if got := Describe(42); got != nil {
		t.Errorf("Describe(int) = %v, want nil", got)
	}
}

func TestDescribe_StructInfoErrorReturnsNil(t *testing.T) {
	type cfg struct {
		A string `env:"FOO"`
		B string `env:"FOO"`
	}
	if got := Describe(cfg{}); got != nil {
		t.Errorf("Describe(conflicting struct) = %v, want nil", got)
	}
}

func TestDescribe_AppliesPrefix(t *testing.T) {
	docs := Describe(describeBasicConfig{}, WithPrefix("APP_"))
	if docs[0].Name != "APP_PORT" {
		t.Errorf("Name = %q, want APP_PORT", docs[0].Name)
	}
}

func TestDescribe_AllFlagsRecorded(t *testing.T) {
	type cfg struct {
		Field string `env:"FIELD,required,notEmpty,secret,immutable"`
	}
	docs := Describe(cfg{})
	if len(docs) != 1 {
		t.Fatalf("got %d docs", len(docs))
	}
	d := docs[0]
	if !d.Required || !d.NotEmpty || !d.Secret || !d.Immutable {
		t.Errorf("flags not all set: %+v", d)
	}
}

func TestDescribe_NestedStruct(t *testing.T) {
	type http struct {
		Port int `env:"PORT,default=8080" envDesc:"HTTP port"`
	}
	type cfg struct {
		HTTP http `envPrefix:"HTTP_"`
	}
	docs := Describe(cfg{})
	if len(docs) != 1 {
		t.Fatalf("got %d docs", len(docs))
	}
	if docs[0].Name != "HTTP_PORT" {
		t.Errorf("nested name = %q", docs[0].Name)
	}
}

func TestDescribe_TypeRendering(t *testing.T) {
	type cfg struct {
		Str  string         `env:"STR"`
		Map  map[string]int `env:"MAP"`
		URL  *url.URL       `env:"URL"`
		Time time.Time      `env:"TIME"`
	}
	docs := Describe(cfg{})
	want := map[string]string{
		"STR":  "string",
		"MAP":  "map[string]int",
		"URL":  "*url.URL",
		"TIME": "time.Time",
	}
	for _, d := range docs {
		if got := want[d.Name]; got != d.GoType {
			t.Errorf("%s GoType = %q, want %q", d.Name, d.GoType, got)
		}
	}
}

// --- PrintUsage ---

func TestPrintUsage_Basic(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintUsage(&buf, describeBasicConfig{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	t.Logf("output:\n%s", out)

	// Header must be present.
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "TYPE") ||
		!strings.Contains(out, "DEFAULT") || !strings.Contains(out, "REQUIRED") ||
		!strings.Contains(out, "DESCRIPTION") {
		t.Errorf("header columns missing")
	}

	// Each field must appear by name.
	for _, want := range []string{"PORT", "DATABASE_URL", "API_KEY", "TIMEOUT", "HOSTS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing field %s", want)
		}
	}

	// Required column annotates DBURL and API_KEY.
	if !strings.Contains(out, "yes") {
		t.Errorf("required \"yes\" missing")
	}

	// Secret annotation in description.
	if !strings.Contains(out, "(secret)") {
		t.Errorf("(secret) marker missing")
	}

	// Defaults present.
	if !strings.Contains(out, "8080") || !strings.Contains(out, "30s") {
		t.Errorf("defaults missing")
	}

	// Description text.
	if !strings.Contains(out, "Primary database DSN") {
		t.Errorf("description text missing")
	}
}

func TestPrintUsage_AlignedColumns(t *testing.T) {
	var buf bytes.Buffer
	type cfg struct {
		Short string `env:"X"`
		Long  string `env:"VERY_LONG_NAME_FOR_ALIGNMENT_TESTING"`
	}
	if err := PrintUsage(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(buf.String(), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	// Header and rows should all start at column 0.
	for i, line := range lines[:3] {
		if line == "" {
			continue
		}
		if line[0] == ' ' {
			t.Errorf("line %d starts with space: %q", i, line)
		}
	}
	// The TYPE header should align with the type values across rows.
	headerTypeIdx := strings.Index(lines[0], "TYPE")
	for i, line := range lines[1:3] {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line[headerTypeIdx:], "string") {
			t.Errorf("row %d not aligned at TYPE col %d: %q", i+1, headerTypeIdx, line)
		}
	}
}

func TestPrintUsage_EmptyStruct(t *testing.T) {
	type cfg struct{}
	var buf bytes.Buffer
	if err := PrintUsage(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty struct should produce empty output, got: %q", buf.String())
	}
}

func TestPrintUsage_NilTarget(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintUsage(&buf, nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestPrintUsage_NilPointer(t *testing.T) {
	var buf bytes.Buffer
	var c *describeBasicConfig
	if err := PrintUsage(&buf, c); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestPrintUsage_NotStruct(t *testing.T) {
	var buf bytes.Buffer
	x := 1
	if err := PrintUsage(&buf, &x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

func TestPrintUsage_StructInfoError(t *testing.T) {
	type cfg struct {
		A string `env:"FOO"`
		B string `env:"FOO"`
	}
	var buf bytes.Buffer
	if err := PrintUsage(&buf, cfg{}); !errors.Is(err, ErrConflictingFields) {
		t.Errorf("err = %v, want ErrConflictingFields", err)
	}
}

func TestPrintUsage_TruncatesLongDefault(t *testing.T) {
	type cfg struct {
		X string `env:"X,default=this-is-a-very-very-very-very-very-long-default-value-that-must-be-truncated"`
	}
	var buf bytes.Buffer
	if err := PrintUsage(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "...") {
		t.Errorf("expected truncation marker in:\n%s", out)
	}
}

func TestPrintUsage_PrefixApplied(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintUsage(&buf, describeBasicConfig{}, WithPrefix("APP_")); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "APP_PORT") {
		t.Errorf("prefix not applied:\n%s", out)
	}
}

func TestPrintUsage_AnnotationOrdering(t *testing.T) {
	type cfg struct {
		A string `env:"A,secret,notEmpty,immutable" envDesc:"compound"`
	}
	var buf bytes.Buffer
	if err := PrintUsage(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Order: (secret) (notEmpty) (immutable) compound
	if !strings.Contains(out, "(secret) (notEmpty) (immutable) compound") {
		t.Errorf("annotation ordering broken:\n%s", out)
	}
}

func TestPrintUsage_AnnotationsWithoutDesc(t *testing.T) {
	type cfg struct {
		A string `env:"A,secret"`
	}
	var buf bytes.Buffer
	if err := PrintUsage(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(secret)") {
		t.Errorf("(secret) marker missing without desc:\n%s", buf.String())
	}
}

// --- Markdown ---

func TestMarkdown_Basic(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, describeBasicConfig{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	t.Logf("output:\n%s", out)

	// Header.
	if !strings.Contains(out, "| Name | Type | Default | Required | Description |") {
		t.Errorf("markdown header missing")
	}
	// Separator row.
	if !strings.Contains(out, "| ---- | ---- |") {
		t.Errorf("markdown separator missing")
	}
	// Backtick-wrapped names.
	if !strings.Contains(out, "`PORT`") || !strings.Contains(out, "`DATABASE_URL`") {
		t.Errorf("backtick-wrapped names missing")
	}
	// Backtick-wrapped types.
	if !strings.Contains(out, "`int`") || !strings.Contains(out, "`time.Duration`") {
		t.Errorf("backtick-wrapped types missing")
	}
	// Required column.
	if !strings.Contains(out, "| yes |") {
		t.Errorf("required column missing")
	}
	// Secret annotation.
	if !strings.Contains(out, "(secret)") {
		t.Errorf("(secret) annotation missing")
	}
	// Default value.
	if !strings.Contains(out, "`8080`") {
		t.Errorf("default value missing")
	}
}

func TestMarkdown_EmptyStruct(t *testing.T) {
	type cfg struct{}
	var buf bytes.Buffer
	if err := Markdown(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty struct should produce empty output, got: %q", buf.String())
	}
}

func TestMarkdown_NilTarget(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, nil); !errors.Is(err, ErrNilPointer) {
		t.Errorf("err = %v, want ErrNilPointer", err)
	}
}

func TestMarkdown_NotStruct(t *testing.T) {
	var buf bytes.Buffer
	x := 1
	if err := Markdown(&buf, &x); !errors.Is(err, ErrNotStruct) {
		t.Errorf("err = %v, want ErrNotStruct", err)
	}
}

func TestMarkdown_PipeInValueEscaped(t *testing.T) {
	type cfg struct {
		X string `env:"X,default=a|b|c" envDesc:"pipe in desc | here"`
	}
	var buf bytes.Buffer
	if err := Markdown(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `a\|b\|c`) {
		t.Errorf("pipe in default not escaped:\n%s", out)
	}
	if !strings.Contains(out, `pipe in desc \| here`) {
		t.Errorf("pipe in desc not escaped:\n%s", out)
	}
}

func TestMarkdown_NoDefault(t *testing.T) {
	type cfg struct {
		X string `env:"X,required"`
	}
	var buf bytes.Buffer
	if err := Markdown(&buf, cfg{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Default cell should be empty when there's no default — no backticks
	// around an empty value.
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "| `X`") {
			// "| `X` | `string` |  | yes | ..." — three spaces around the empty cell.
			if strings.Contains(line, "`` ") {
				t.Errorf("empty default rendered with backticks: %q", line)
			}
		}
	}
}

// --- Loader methods ---

func TestLoaderMethod_Describe(t *testing.T) {
	loader := New(WithPrefix("X_"))
	docs := loader.Describe(describeBasicConfig{})
	if docs[0].Name != "X_PORT" {
		t.Errorf("loader.Describe prefix not applied: %q", docs[0].Name)
	}
}

func TestLoaderMethod_PrintUsage(t *testing.T) {
	loader := New(WithPrefix("X_"))
	var buf bytes.Buffer
	if err := loader.PrintUsage(&buf, describeBasicConfig{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "X_PORT") {
		t.Errorf("loader.PrintUsage prefix not applied:\n%s", buf.String())
	}
}

func TestLoaderMethod_Markdown(t *testing.T) {
	loader := New(WithPrefix("X_"))
	var buf bytes.Buffer
	if err := loader.Markdown(&buf, describeBasicConfig{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "`X_PORT`") {
		t.Errorf("loader.Markdown prefix not applied:\n%s", buf.String())
	}
}

// --- truncate / escapeMD helpers ---

func TestTruncate(t *testing.T) {
	cases := []struct {
		in  string
		max int
		out string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hellothere", 5, "he..."},
		{"hellothere", 4, "h..."},
		{"hello", 3, "hel"},
		{"hello", 0, "hello"}, // disabled
		{"", 5, ""},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.out {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.out)
		}
	}
}

func TestEscapeMD(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"with|pipe", `with\|pipe`},
		{"a|b|c", `a\|b\|c`},
		{"", ""},
	}
	for _, tc := range cases {
		if got := escapeMD(tc.in); got != tc.want {
			t.Errorf("escapeMD(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- write-error paths ---

// describeFailingWriter returns an error after writing N bytes; allowance=0
// fails on every write.
type describeFailingWriter struct {
	allowance int
}

var errDescribeWriteFail = errors.New("describe writer failure")

func (w *describeFailingWriter) Write(p []byte) (int, error) {
	if w.allowance <= 0 {
		return 0, errDescribeWriteFail
	}
	if len(p) <= w.allowance {
		w.allowance -= len(p)
		return len(p), nil
	}
	n := w.allowance
	w.allowance = 0
	return n, errDescribeWriteFail
}

func TestPrintUsage_HeaderWriteError(t *testing.T) {
	if err := PrintUsage(&describeFailingWriter{}, describeBasicConfig{}); err == nil {
		t.Fatal("expected error from failing writer on header")
	}
}

func TestPrintUsage_RowWriteError(t *testing.T) {
	// Allow the header through, fail on the first row.
	w := &describeFailingWriter{allowance: 200}
	err := PrintUsage(w, describeBasicConfig{})
	if err == nil {
		t.Fatal("expected error from failing writer on row")
	}
}

func TestMarkdown_HeaderWriteError(t *testing.T) {
	if err := Markdown(&describeFailingWriter{}, describeBasicConfig{}); err == nil {
		t.Fatal("expected error from failing writer on header")
	}
}

func TestMarkdown_SeparatorWriteError(t *testing.T) {
	// Allow the header to write, fail on the separator.
	w := &describeFailingWriter{allowance: 60}
	err := Markdown(w, describeBasicConfig{})
	if err == nil {
		t.Fatal("expected error from failing writer on separator")
	}
}

func TestMarkdown_RowWriteError(t *testing.T) {
	// Allow header + separator through, fail on the first row.
	w := &describeFailingWriter{allowance: 130}
	err := Markdown(w, describeBasicConfig{})
	if err == nil {
		t.Fatal("expected error from failing writer on row")
	}
}

// --- composeDescription ---

func TestComposeDescription(t *testing.T) {
	cases := []struct {
		name string
		in   FieldDoc
		want string
	}{
		{"plain", FieldDoc{Description: "hello"}, "hello"},
		{"only flags", FieldDoc{Secret: true, NotEmpty: true}, "(secret) (notEmpty)"},
		{"flags + desc", FieldDoc{Secret: true, Description: "hi"}, "(secret) hi"},
		{"all flags", FieldDoc{Secret: true, NotEmpty: true, Immutable: true, Description: "x"},
			"(secret) (notEmpty) (immutable) x"},
		{"empty", FieldDoc{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDescription(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
