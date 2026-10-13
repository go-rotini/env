package env

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExpandFieldValue(t *testing.T) {
	lk := func(k string) (string, bool) {
		v, ok := map[string]string{"A": "alice", "B": "bob", "EMPTY": ""}[k]
		return v, ok
	}

	ok := []struct{ in, want string }{
		{"plain", "plain"},
		{"$A", "alice"},
		{"${A}", "alice"},
		{"x-${A}-y", "x-alice-y"},
		{"$A and $B", "alice and bob"},
		{"${A}${B}", "alicebob"},
		{"${A}1", "alice1"},
		{"$MISSING", ""},
		{"${MISSING}", ""},
		{"a${MISSING}b", "ab"},
		{`\$A`, "$A"},
		{`pre \$A post`, "pre $A post"},
		{"$", "$"},
		{"$ ", "$ "},
		{"$.x", "$.x"},
		{"$$", "$$"},
		{"${UNCLOSED", "${UNCLOSED"},
		{"${}", ""},
		{"${A-x}", "alice"},
		{"${MISSING-x}", "x"},
		{"${EMPTY-x}", ""}, // bare '-': only an *unset* var triggers the default
		{"${A:-x}", "alice"},
		{"${MISSING:-x}", "x"},
		{"${EMPTY:-x}", "x"}, // ':-': empty also triggers the default
		{"${MISSING:-${B}}", "bob"},
		{"${MISSING:-${ALSO:-final}}", "final"},
		{"${MISSING:-${A}-${B}}", "alice-bob"},
		{"${MISSING:-a{b}c}", "a{b}c"},       // balanced braces inside the default
		{`${MISSING:-x\}y}`, `x\}y`},         // escaped '}' inside the default
		{"${MISSING-}", ""},                  // bare '-', empty default
		{"${MISSING:-}", ""},                 // ':-', empty default
		{"${a{b}-x}", "x"},                   // braces in the *name* part (before the operator)
		{`${a\b-x}`, "x"},                    // escaped byte in the name part
		{"${a:z-x}", "x"},                    // ':' not followed by '-'/'?' is part of the name
		{"${A:-${MISSING:?never}}", "alice"}, // A set → the :? in the unused default never fires
		{"${A:?should not fire}", "alice"},
		{"${EMPTY:?should not fire}", ""}, // ':?' triggers on *unset*, not empty
	}
	for _, c := range ok {
		got, err := expandFieldValue(c.in, "F", lk)
		if err != nil {
			t.Errorf("expandFieldValue(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("expandFieldValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// ${VAR:?message} for an unset VAR → error wrapping ErrExpansionAssertion.
	_, err := expandFieldValue("host=${MISSING:?must be set}", "DSN", lk)
	if !errors.Is(err, ErrExpansionAssertion) {
		t.Fatalf("err = %v, want wrapping ErrExpansionAssertion", err)
	}
	for _, want := range []string{"must be set", "DSN", "MISSING"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err message %q missing %q", err, want)
		}
	}
	// Empty message still errors (with a default message).
	if _, err := expandFieldValue("${MISSING:?}", "F", lk); !errors.Is(err, ErrExpansionAssertion) {
		t.Errorf("empty-message :? : err = %v, want ErrExpansionAssertion", err)
	}

	// Excessive nesting of defaults (the var is never set, so every level
	// recurses) → *CycleError.
	deep := strings.Repeat("${UNDEF:-", 40) + "x" + strings.Repeat("}", 40)
	if _, err := expandFieldValue(deep, "F", lk); !errors.Is(err, ErrCycle) {
		t.Errorf("deep nesting: err = %v, want *CycleError", err)
	}
}

func TestLoad_ExpandTag(t *testing.T) {
	src := Map(map[string]string{
		"DB_USER": "alice",
		"DB_PASS": "s3cret",
		"DB_HOST": "db.local",
		"DSN":     "postgres://${DB_USER}:${DB_PASS}@${DB_HOST}/app",
		"PLAIN":   "${DB_USER}", // no expand tag below → stays literal
		"WITHDEF": "${MISSING:-fallback}",
		"PORT":    "${MISSING:-8080}",
	})
	loader := New(WithSource(src))

	type cfg struct {
		DSN      string `env:"DSN,expand"`
		Plain    string `env:"PLAIN"`
		WithDef  string `env:"WITHDEF,expand"`
		Port     int    `env:"PORT,expand"`
		Greeting string `env:"GREETING,default=hi ${DB_USER},expand"`
	}
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DSN != "postgres://alice:s3cret@db.local/app" {
		t.Errorf("DSN = %q", c.DSN)
	}
	if c.Plain != "${DB_USER}" {
		t.Errorf("Plain = %q, want literal (no expand tag)", c.Plain)
	}
	if c.WithDef != "fallback" {
		t.Errorf("WithDef = %q", c.WithDef)
	}
	if c.Port != 8080 {
		t.Errorf("Port = %d", c.Port)
	}
	if c.Greeting != "hi alice" {
		t.Errorf("Greeting = %q, want \"hi alice\" (default should be expanded)", c.Greeting)
	}
}

func TestLoad_ExpandTag_NotEmptyAfterExpansion(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"X": "${MISSING}"})))
	var c struct {
		X string `env:"X,expand,notEmpty"`
	}
	if err := loader.Load(&c); !errors.Is(err, ErrEmptyValue) {
		t.Errorf("err = %v, want ErrEmptyValue (expanded to empty)", err)
	}
}

func TestLoad_ExpandTag_AssertionFailure(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"DSN": "h=${REQ_HOST:?set REQ_HOST}"})))
	var c struct {
		DSN string `env:"DSN,expand"`
	}
	if err := loader.Load(&c); !errors.Is(err, ErrExpansionAssertion) {
		t.Errorf("err = %v, want ErrExpansionAssertion", err)
	}
}

func TestLoad_ExpandTag_Cycle(t *testing.T) {
	deep := strings.Repeat("${UNDEF:-", 40) + "x" + strings.Repeat("}", 40)
	loader := New(WithSource(Map(map[string]string{"DSN": deep})))
	var c struct {
		DSN string `env:"DSN,expand"`
	}
	if err := loader.Load(&c); !errors.Is(err, ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestExpandFieldValue_UnterminatedBracesLinear(t *testing.T) {
	t.Parallel()
	lk := func(string) (string, bool) { return "", false }
	text := strings.Repeat("${", 100_000)
	start := time.Now()
	got, err := expandFieldValue(text, "F", lk)
	if err != nil || got != text {
		t.Fatalf("got len %d, err %v", len(got), err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("200 KB of unterminated ${ took %v", d)
	}
}

// matchBraceScan is the per-reference scan matchBraces replaces: the
// index in s of the `}` matching an implicit outer `{`, or -1.
func matchBraceScan(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

func FuzzMatchBraces(f *testing.F) {
	for _, s := range []string{"${A}", "${a{b ${c}", `${\}}`, "${${${", "x${A:-${B}}y}", `\${A}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		closes := matchBraces(s, nil)
		for i := 0; i+1 < len(s); i++ {
			if s[i] != '$' || s[i+1] != '{' {
				continue
			}
			want := matchBraceScan(s[i+2:])
			if want >= 0 {
				want += i + 2
			}
			if got := closes.closeOf(i + 1); got != want {
				t.Fatalf("%q at %d: matchBraces %d, scan %d", s, i, got, want)
			}
		}
	})
}

func FuzzExpandFieldValue(f *testing.F) {
	for _, s := range []string{"$A", "${A}", "${A:-${B:-x}}", "${A:?m}", `\$A`, "${", "${A-}", `${A:-\}}`} {
		f.Add(s, "va")
	}
	f.Fuzz(func(t *testing.T, v, a string) {
		if len(v) > 4096 {
			return
		}
		lk := func(k string) (string, bool) {
			switch k {
			case "A":
				return a, true
			case "B":
				return v, true
			}
			return "", false
		}
		_, _ = expandFieldValue(v, "F", lk)
	})
}
