package dotenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rotini/env"
)

// --- Load (file → map) ---

func TestLoad_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	contents := "FOO=bar\nBAZ=qux\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "bar" || got["BAZ"] != "qux" {
		t.Errorf("got %+v", got)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/no/such/file.env")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("123FOO=bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- Parse (bytes → File) ---

func TestParse_HappyPath(t *testing.T) {
	f, err := Parse([]byte("FOO=bar\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("FOO"); v != "bar" {
		t.Errorf("got %q", v)
	}
}

// --- NewSource (file → env.Source) ---

func TestNewSource_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("FOO=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := src.Lookup("FOO"); !ok || v != "from-file" {
		t.Errorf("got (%q, %v)", v, ok)
	}
	if _, ok := src.Lookup("MISSING"); ok {
		t.Error("missing key returned ok=true")
	}
}

func TestNewSource_ImplementsEnvSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("FOO=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(path)
	if err != nil {
		t.Fatal(err)
	}
	// Compile-time assertion: the interface value satisfies env.Source.
	var _ env.Source = src
}

func TestNewSource_FileNotFound(t *testing.T) {
	_, err := NewSource("/no/such/file.env")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewSource_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("123FOO=bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewSource(path)
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- NewReaderSource ---

func TestNewReaderSource_HappyPath(t *testing.T) {
	r := strings.NewReader("FOO=from-reader\n")
	src, err := NewReaderSource(r)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := src.Lookup("FOO"); v != "from-reader" {
		t.Errorf("got %q", v)
	}
}

func TestNewReaderSource_ParseError(t *testing.T) {
	r := strings.NewReader("123FOO=bar\n")
	_, err := NewReaderSource(r)
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

// --- Loader integration ---

func TestNewSource_IntegratesWithLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("DATABASE_URL=postgres://from-file\nPORT=9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(path)
	if err != nil {
		t.Fatal(err)
	}

	type cfg struct {
		Port  int    `env:"PORT,default=8080"`
		DBURL string `env:"DATABASE_URL,required"`
	}
	loader := env.New(env.WithSource(env.OSEnv(), src))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.Port != 9000 {
		t.Errorf("Port = %d", c.Port)
	}
	if c.DBURL != "postgres://from-file" {
		t.Errorf("DBURL = %q", c.DBURL)
	}
}

func TestNewSource_OSEnvWinsOverDotenv(t *testing.T) {
	const key = "ENVTEST_DOTENV_PRECEDENCE"
	t.Setenv(key, "from-osenv")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(key+"=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(path)
	if err != nil {
		t.Fatal(err)
	}

	type cfg struct {
		V string `env:"ENVTEST_DOTENV_PRECEDENCE"`
	}
	// OSEnv first; dotenv as fallback.
	loader := env.New(env.WithSource(env.OSEnv(), src))
	var c cfg
	if err := loader.Load(&c); err != nil {
		t.Fatal(err)
	}
	if c.V != "from-osenv" {
		t.Errorf("V = %q (OSEnv should win)", c.V)
	}
}
