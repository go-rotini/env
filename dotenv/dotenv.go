package dotenv

import (
	"fmt"
	"io"
	"os"

	"github.com/go-rotini/env"
)

// Parse parses data as a `.env` byte stream and returns the resulting
// AST. Use [File.Get] / [File.Keys] / [File.ToMap] for access, or
// [File.Marshal] for round-trip emission.
func Parse(data []byte, opts ...ParseOption) (*File, error) {
	o := defaultParseOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return parse(data, o)
}

// Load reads a `.env` file from path and returns its parsed key/value
// map (last-write-wins per Bash convention).
func Load(path string, opts ...ParseOption) (map[string]string, error) {
	o := defaultParseOptions()
	for _, opt := range opts {
		opt(&o)
	}
	o.filePath = path

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dotenv: read %s: %w", path, err)
	}
	f, err := parse(data, o)
	if err != nil {
		return nil, err
	}
	return f.ToMap(), nil
}

// NewSource reads a `.env` file from path and returns an [env.Source]
// that consults its parsed key/value map. Subsequent file changes do
// NOT propagate; the source is a snapshot taken at construction.
//
// For reload-aware sources, use the env/dotenv/watcher sub-package
// (added in Phase 10).
func NewSource(path string, opts ...ParseOption) (env.Source, error) {
	o := defaultParseOptions()
	for _, opt := range opts {
		opt(&o)
	}
	o.filePath = path

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dotenv: read %s: %w", path, err)
	}
	f, err := parse(data, o)
	if err != nil {
		return nil, err
	}
	return &fileSource{m: f.ToMap()}, nil
}

// NewReaderSource reads a `.env` byte stream from r and returns an
// [env.Source] that consults its parsed key/value map.
func NewReaderSource(r io.Reader, opts ...ParseOption) (env.Source, error) {
	o := defaultParseOptions()
	for _, opt := range opts {
		opt(&o)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("dotenv: read: %w", err)
	}
	f, err := parse(data, o)
	if err != nil {
		return nil, err
	}
	return &fileSource{m: f.ToMap()}, nil
}

// fileSource wraps a parsed `.env` map as an [env.Source]. The map is
// captured at construction; the Source is read-only and concurrent-safe.
type fileSource struct {
	m map[string]string
}

func (s *fileSource) Lookup(key string) (string, bool) {
	v, ok := s.m[key]
	return v, ok
}

// Compile-time check: fileSource satisfies env.Source.
var _ env.Source = (*fileSource)(nil)

// (Watcher is intentionally NOT implemented here; it lives in the
// env/dotenv/watcher sub-package which adds the fsnotify dependency.)
