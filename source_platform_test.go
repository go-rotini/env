package env_test

import (
	"runtime"
	"testing"

	"github.com/go-rotini/env"
)

// Cross-platform OSEnv tests. These match the platform-specific
// behavior matrix documented in §8.6 of the design doc:
//
//	Ubuntu / macOS — POSIX, case-sensitive identifiers.
//	Windows        — case-insensitive identifiers; `=C:`-style hidden
//	                 variables that os.Environ exposes are filtered.
//
// Each test gates on runtime.GOOS so the matrix runs the right
// assertions per platform without skipping anything by accident.

// TestUnixCaseSensitive verifies that on POSIX systems
// OSEnv().Lookup distinguishes case. Windows folds case at the OS
// layer, so this test is gated to non-Windows.
func TestUnixCaseSensitive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows folds case at the OS layer; see TestWindowsCaseInsensitive")
	}
	t.Setenv("ROTINI_CASE_PROBE", "set-as-upper")
	src := env.OSEnv()
	if v, ok := src.Lookup("ROTINI_CASE_PROBE"); !ok || v != "set-as-upper" {
		t.Errorf("uppercase: (%q, %v), want (set-as-upper, true)", v, ok)
	}
	if _, ok := src.Lookup("rotini_case_probe"); ok {
		t.Error("lowercase: ok=true, want false (POSIX is case-sensitive)")
	}
}

// TestWindowsCaseInsensitive verifies that on Windows OSEnv().Lookup
// folds case (matching the platform's convention). Skipped on
// POSIX.
func TestWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only: POSIX kernels are case-sensitive")
	}
	t.Setenv("ROTINI_CASE_PROBE", "case-folded")
	src := env.OSEnv()
	if v, ok := src.Lookup("ROTINI_CASE_PROBE"); !ok || v != "case-folded" {
		t.Errorf("uppercase: (%q, %v)", v, ok)
	}
	if v, ok := src.Lookup("rotini_case_probe"); !ok || v != "case-folded" {
		t.Errorf("lowercase should still resolve on Windows: (%q, %v)", v, ok)
	}
}

// TestWindowsHiddenVars verifies that the leading-`=` per-drive
// hidden variables Windows exposes via os.Environ are filtered by
// OSEnv().Lookup so callers never observe them. Documented in §2.1.4.
func TestWindowsHiddenVars(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only behavior")
	}
	src := env.OSEnv()
	// The exact set of hidden variables differs by drive layout. We
	// can't predict their names; we can only assert that any name
	// beginning with `=` is rejected without consulting the OS.
	for _, key := range []string{"=C:", "=D:", "=ExitCode"} {
		if _, ok := src.Lookup(key); ok {
			t.Errorf("Lookup(%q) returned ok=true; OSEnv must reject leading-= keys", key)
		}
	}
}
