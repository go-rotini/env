package dotenv

import (
	"errors"
	"testing"
)

func TestLint_PortableSubsetCleanly(t *testing.T) {
	src := []byte(`# this file is portable
FOO=bar
BAZ="quoted value"
EMPTY=
`)
	issues, err := Lint(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no warnings, got %v", issues)
	}
}

func TestLint_ExportPrefixWarns(t *testing.T) {
	src := []byte("export FOO=bar\n")
	issues, err := Lint(src)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "export-prefix") {
		t.Errorf("expected export-prefix warning, got %v", issues)
	}
}

func TestLint_YAMLSeparatorWarns(t *testing.T) {
	src := []byte("FOO: bar\n")
	issues, err := Lint(src)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "yaml-separator") {
		t.Errorf("expected yaml-separator warning, got %v", issues)
	}
}

func TestLint_NonPOSIXIdentifierWarns(t *testing.T) {
	src := []byte("FOO-BAR=1\n")
	issues, err := Lint(src, WithRelaxedNames())
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "non-posix-identifier") {
		t.Errorf("expected non-posix-identifier warning, got %v", issues)
	}
}

func TestLint_BacktickValueWarns(t *testing.T) {
	src := []byte("FOO=`literal`\n")
	issues, err := Lint(src, WithBackticks())
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "backtick-value") {
		t.Errorf("expected backtick-value warning, got %v", issues)
	}
}

func TestLint_SyntaxErrorReportedAsErrorSeverity(t *testing.T) {
	src := []byte("123FOO=bar\n")
	issues, err := Lint(src)
	if err == nil {
		t.Fatal("expected parse error")
	}
	// The syntax error is also surfaced as a SeverityError lint issue.
	gotSyntax := false
	for _, iss := range issues {
		if iss.Severity == SeverityError {
			gotSyntax = true
			break
		}
	}
	if !gotSyntax {
		t.Errorf("expected SeverityError lint issue, got: %v", issues)
	}
	// And the underlying error type is ErrParse.
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestLint_EmptyInput(t *testing.T) {
	issues, err := Lint(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("got %v, want empty", issues)
	}
}

func TestSeverity_String(t *testing.T) {
	cases := map[Severity]string{
		SeverityError:   "error",
		SeverityWarning: "warning",
		Severity(99):    "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func hasIssue(issues []LintIssue, rule string) bool {
	for _, iss := range issues {
		if iss.Rule == rule {
			return true
		}
	}
	return false
}
