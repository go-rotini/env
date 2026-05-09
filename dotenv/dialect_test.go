package dotenv_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rotini/env/dotenv"
)

// TestDialectCompat runs the parser against the vendored dialect
// fixtures in testdata/dialect/{bkeepers,motdotla,compose,foreman}/
// and asserts that the parsed map equals the fixture's
// `*_expected.json` companion. Each dialect supplies its own parse
// options so the test mirrors the behavior a user would observe in
// the real tool.
//
// Fixtures are vendored (not cloned at build time) because none of
// the upstream projects publishes a versioned standalone test corpus.
func TestDialectCompat(t *testing.T) {
	t.Parallel()
	root := dialectRoot(t)

	dialects := []struct {
		name string
		opts []dotenv.ParseOption
	}{
		{"bkeepers", []dotenv.ParseOption{dotenv.WithExpand()}},
		{"motdotla", nil},
		{"compose", nil},
		{"foreman", nil},
	}

	for _, d := range dialects {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			runDialectFixtures(t, filepath.Join(root, d.name), d.opts)
		})
	}
}

type expectedFixture struct {
	Result map[string]string `json:"result"`
	Error  *string           `json:"error"`
}

func runDialectFixtures(t *testing.T, dir string, opts []dotenv.ParseOption) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}

	fixtures := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".env") {
			continue
		}
		fixtures++
		name := strings.TrimSuffix(e.Name(), ".env")
		envPath := filepath.Join(dir, e.Name())
		expectedPath := filepath.Join(dir, name+"_expected.json")

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runOneDialectFixture(t, envPath, expectedPath, opts)
		})
	}
	if fixtures == 0 {
		t.Fatalf("no *.env fixtures in %q", dir)
	}
}

func runOneDialectFixture(t *testing.T, envPath, expectedPath string, opts []dotenv.ParseOption) {
	t.Helper()
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", envPath, err)
	}

	expected := loadExpected(t, expectedPath)

	file, parseErr := dotenv.Parse(envBytes, opts...)
	if expected.Error != nil {
		if parseErr == nil {
			t.Errorf("expected error %q, got nil; map=%v", *expected.Error, file.ToMap())
		}
		return
	}
	if parseErr != nil {
		t.Fatalf("Parse: %v", parseErr)
	}
	got := file.ToMap()
	if !mapsEqual(got, expected.Result) {
		t.Errorf("parse map mismatch:\n  got=%v\n want=%v", got, expected.Result)
	}
}

func loadExpected(t *testing.T, path string) expectedFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var out expectedFixture
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal(%q): %v", path, err)
	}
	return out
}

// mapsEqual compares two string maps by content. Used instead of
// reflect.DeepEqual so the test failure message is human-readable
// when the maps diverge.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}

// dialectRoot resolves the absolute path to testdata/dialect/, which
// lives at the module root (one directory above the dotenv package).
func dialectRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "testdata", "dialect")
}
