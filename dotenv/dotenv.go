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

// Load reads a `.env` file from path and returns its key/value map
// (last-write-wins per Bash convention).
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
// over its parsed map. The source is a snapshot taken at construction;
// subsequent file changes do not propagate. For live reload, use the
// env/dotenv/watcher sub-package.
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
// [env.Source] over its parsed map.
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

type fileSource struct {
	m map[string]string
}

func (s *fileSource) Lookup(key string) (string, bool) {
	v, ok := s.m[key]
	return v, ok
}

var _ env.Source = (*fileSource)(nil)
