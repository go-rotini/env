package dotenv

import (
	"errors"
	"testing"

	"github.com/go-rotini/env"
)

// --- $VAR / ${VAR} basic forms ---

func TestExpand_BareDollar(t *testing.T) {
	src := "FIRST=hello\nSECOND=$FIRST\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("SECOND"); v != "hello" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_BracedDollar(t *testing.T) {
	src := "FIRST=hello\nSECOND=${FIRST}_world\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("SECOND"); v != "hello_world" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_DoubleQuoted(t *testing.T) {
	src := `FIRST=hello` + "\n" + `SECOND="prefix-${FIRST}-suffix"` + "\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("SECOND"); v != "prefix-hello-suffix" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_SingleQuotedNotExpanded(t *testing.T) {
	src := "FIRST=hello\nSECOND='${FIRST}'\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("SECOND"); v != "${FIRST}" {
		t.Errorf("got %q (single-quoted should not expand)", v)
	}
}

func TestExpand_BackslashDollarSuppressed(t *testing.T) {
	src := `FIRST=hello` + "\n" + `SECOND="\${FIRST}"` + "\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("SECOND"); v != "${FIRST}" {
		t.Errorf("got %q (\\$ should suppress expansion)", v)
	}
}

func TestExpand_LoneDollar(t *testing.T) {
	src := "FOO=$\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "$" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_DollarFollowedByNonIdent(t *testing.T) {
	src := "FOO=$.bar\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "$.bar" {
		t.Errorf("got %q", v)
	}
}

// --- :- and - default forms ---

func TestExpand_ColonDashUnsetUsesDefault(t *testing.T) {
	src := `FOO=${MISSING:-fallback}` + "\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "fallback" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_ColonDashEmptyUsesDefault(t *testing.T) {
	src := "EMPTY=\nFOO=${EMPTY:-fallback}\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "fallback" {
		t.Errorf("got %q (:- should treat empty as unset)", v)
	}
}

func TestExpand_DashUnsetUsesDefault(t *testing.T) {
	src := `FOO=${MISSING-fallback}` + "\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "fallback" {
		t.Errorf("got %q", v)
	}
}

func TestExpand_DashEmptyKeepsEmpty(t *testing.T) {
	// POSIX `${VAR-default}`: empty stays empty (only unset triggers default).
	src := "EMPTY=\nFOO=${EMPTY-fallback}\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "" {
		t.Errorf("got %q (- with empty should NOT use default)", v)
	}
}

func TestExpand_DefaultIsExpanded(t *testing.T) {
	src := "INNER=resolved\nFOO=${MISSING:-${INNER}}\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "resolved" {
		t.Errorf("got %q (default should be recursively expanded)", v)
	}
}

// --- :? assertion form ---

func TestExpand_ColonQuestionUnsetErrors(t *testing.T) {
	src := `FOO=${MISSING:?required for foo}` + "\n"
	_, err := Parse([]byte(src), WithExpand())
	if !errors.Is(err, env.ErrExpansionAssertion) {
		t.Errorf("err = %v, want ErrExpansionAssertion", err)
	}
	if !errors.Is(err, ErrExpansion) {
		t.Errorf("err is not ExpansionError")
	}
}

func TestExpand_ColonQuestionSetSucceeds(t *testing.T) {
	src := "FOO=present\nBAR=${FOO:?required}\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("BAR"); v != "present" {
		t.Errorf("got %q", v)
	}
}

// --- WithStrictExpansion ---

func TestExpand_StrictExpansion(t *testing.T) {
	src := `FOO=${MISSING}` + "\n"
	_, err := Parse([]byte(src), WithExpand(), WithStrictExpansion())
	if !errors.Is(err, env.ErrUnsetExpansion) {
		t.Errorf("err = %v, want ErrUnsetExpansion", err)
	}
}

func TestExpand_LenientUnsetExpandsToEmpty(t *testing.T) {
	src := `FOO=${MISSING}` + "\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "" {
		t.Errorf("got %q", v)
	}
}

// --- WithExpansionParent ---

func TestExpand_ExpansionParent(t *testing.T) {
	parent := env.Map(map[string]string{"FROM_PARENT": "the-value"})
	src := `FOO=${FROM_PARENT}` + "\n"
	f := mustParse(t, src, WithExpand(), WithExpansionParent(parent))
	if v, _ := f.Get("FOO"); v != "the-value" {
		t.Errorf("got %q (parent lookup failed)", v)
	}
}

func TestExpand_FilePrecedesParent(t *testing.T) {
	parent := env.Map(map[string]string{"X": "from-parent"})
	src := "X=from-file\nFOO=${X}\n"
	f := mustParse(t, src, WithExpand(), WithExpansionParent(parent))
	if v, _ := f.Get("FOO"); v != "from-file" {
		t.Errorf("got %q (file should win over parent)", v)
	}
}

// --- WithMaxExpansionDepth ---

func TestExpand_MaxDepth(t *testing.T) {
	src := "FOO=${MISSING:-${MISSING:-${MISSING:-${MISSING:-deep}}}}\n"
	_, err := Parse([]byte(src), WithExpand(), WithMaxExpansionDepth(2))
	if !errors.Is(err, ErrExpansion) {
		t.Errorf("err = %v, want ExpansionError", err)
	}
}

// --- expansion not running without WithExpand ---

func TestExpand_NotRunByDefault(t *testing.T) {
	src := `FOO=${MISSING}` + "\n"
	f := mustParse(t, src) // no WithExpand
	if v, _ := f.Get("FOO"); v != "${MISSING}" {
		t.Errorf("got %q (expansion ran without WithExpand)", v)
	}
}

// --- multiple variables in one value ---

func TestExpand_MultipleInOneValue(t *testing.T) {
	src := "A=1\nB=2\nC=$A-$B-${A}-${B}\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("C"); v != "1-2-1-2" {
		t.Errorf("got %q", v)
	}
}

// --- unterminated brace ---

func TestExpand_UnterminatedBraceLeftLiteral(t *testing.T) {
	src := "FOO=${UNCLOSED\n"
	f := mustParse(t, src, WithExpand())
	if v, _ := f.Get("FOO"); v != "${UNCLOSED" {
		t.Errorf("got %q (unterminated ${ should be literal)", v)
	}
}

// --- low-level helpers ---

func TestParseVarRef_BareName(t *testing.T) {
	r, ok := parseVarRef("$NAME rest")
	if !ok || r.name != "NAME" || r.consumed != 5 {
		t.Errorf("got %+v ok=%v", r, ok)
	}
}

func TestParseVarRef_BracedNoOp(t *testing.T) {
	r, ok := parseVarRef("${NAME} rest")
	if !ok || r.name != "NAME" || r.consumed != 7 {
		t.Errorf("got %+v ok=%v", r, ok)
	}
}

func TestParseVarRef_ShortInputRejected(t *testing.T) {
	if _, ok := parseVarRef(""); ok {
		t.Error("empty input should not parse")
	}
	if _, ok := parseVarRef("$"); ok {
		t.Error("lone $ should not parse")
	}
	if _, ok := parseVarRef("X"); ok {
		t.Error("non-$ start should not parse")
	}
	if _, ok := parseVarRef("$."); ok {
		t.Error("$<non-ident> should not parse")
	}
}

func TestParseVarRef_UnterminatedBrace(t *testing.T) {
	if _, ok := parseVarRef("${UNCLOSED"); ok {
		t.Error("unterminated ${ should not parse")
	}
}

func TestFindMatchingBrace(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a}b", 1},
		{"{nested}}rest", 8},
		{`a\}b}`, 4}, // \} is escaped, doesn't count
		{"unclosed", -1},
	}
	for _, c := range cases {
		if got := findMatchingBrace(c.in); got != c.want {
			t.Errorf("findMatchingBrace(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFirstTopLevelOperator(t *testing.T) {
	cases := []struct {
		in    string
		idx   int
		oplen int
		op    string
	}{
		{"NAME:-default", 4, 2, ":-"},
		{"NAME:?required", 4, 2, ":?"},
		{"NAME-default", 4, 1, "-"},
		{"NAME${INNER:-x}", -1, 0, ""},          // operator inside nested ${}
		{`NAME\:?escaped`, -1, 0, ""},           // escaped doesn't count
		{"NAME", -1, 0, ""},                     // no operator
		{"NAME{nested-thing}-real", 18, 1, "-"}, // skip nested braces
	}
	for _, c := range cases {
		gotIdx, gotLen, gotOp := firstTopLevelOperator(c.in)
		if gotIdx != c.idx || gotLen != c.oplen || gotOp != c.op {
			t.Errorf("firstTopLevelOperator(%q) = (%d, %d, %q), want (%d, %d, %q)",
				c.in, gotIdx, gotLen, gotOp, c.idx, c.oplen, c.op)
		}
	}
}

func TestIsVarRefByte(t *testing.T) {
	for _, c := range "ABCabc012_" {
		if !isVarRefByte(byte(c)) {
			t.Errorf("isVarRefByte(%q) = false, want true", c)
		}
	}
	for _, c := range "-.=#$" {
		if isVarRefByte(byte(c)) {
			t.Errorf("isVarRefByte(%q) = true, want false", c)
		}
	}
}
