package dotenv

import (
	"errors"
	"fmt"

	"github.com/go-rotini/env"
)

// ParseError is returned by [Parse] / [Load] / [NewSource] when the
// `.env` source is malformed. It carries the file path (when available)
// and the line/column position of the failure.
type ParseError struct {
	Pos     env.Position
	Message string
}

func (e *ParseError) Error() string {
	if e == nil {
		return "dotenv: parse error"
	}
	if e.Pos.Line > 0 {
		if e.Pos.File != "" {
			return fmt.Sprintf("dotenv: %s:%d:%d: %s", e.Pos.File, e.Pos.Line, e.Pos.Column, e.Message)
		}
		return fmt.Sprintf("dotenv: line %d, column %d: %s", e.Pos.Line, e.Pos.Column, e.Message)
	}
	return fmt.Sprintf("dotenv: %s", e.Message)
}

func (*ParseError) Is(target error) bool {
	_, ok := target.(*ParseError)
	return ok
}

// ExpansionError is returned when variable expansion fails: an unset
// variable under [WithStrictExpansion], a `${VAR:?msg}` assertion
// failure, a depth-bound exceeded, or a reference cycle.
type ExpansionError struct {
	Pos      env.Position
	Variable string
	Reason   string
	Cause    error
}

func (e *ExpansionError) Error() string {
	if e == nil {
		return "dotenv: expansion error"
	}
	switch {
	case e.Pos.Line > 0:
		return fmt.Sprintf("dotenv: line %d: expansion of %q: %s", e.Pos.Line, e.Variable, e.Reason)
	case e.Variable != "":
		return fmt.Sprintf("dotenv: expansion of %q: %s", e.Variable, e.Reason)
	default:
		return fmt.Sprintf("dotenv: expansion: %s", e.Reason)
	}
}

func (*ExpansionError) Is(target error) bool {
	_, ok := target.(*ExpansionError)
	return ok
}

func (e *ExpansionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Sentinel errors. The struct-pointer sentinels match by type via the
// corresponding Is method; the others match by value identity.
var (
	// ErrParse matches any [*ParseError].
	ErrParse = &ParseError{}

	// ErrExpansion matches any [*ExpansionError].
	ErrExpansion = &ExpansionError{}

	// ErrFileTooLarge is returned when an input exceeds [WithMaxFileSize].
	ErrFileTooLarge = errors.New("dotenv: file size exceeds limit")

	// ErrLineTooLong is returned when a single line exceeds [WithMaxLineLength].
	ErrLineTooLong = errors.New("dotenv: line length exceeds limit")
)
