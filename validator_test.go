package env

import (
	"errors"
	"testing"
)

// --- Validator interface basics ---

type validatorOK struct {
	X int `env:"X,default=1"`
}

func (*validatorOK) Validate() error { return nil }

func TestValidator_Success(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{})))
	var c validatorOK
	if err := loader.Load(&c); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

type validatorFail struct {
	Host string `env:"HOST,default=localhost"`
	Port int    `env:"PORT,default=8080"`
}

func (v *validatorFail) Validate() error {
	if v.Port < 1 || v.Port > 65535 {
		return errors.New("port out of range")
	}
	return nil
}

func TestValidator_Failure(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{"PORT": "99999"})))
	var c validatorFail
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

// --- skip on field errors ---

type validatorWithRequiredField struct {
	X string `env:"X,required"` // field-level error if unset
}

var validatorWithRequiredFieldCalls int

func (*validatorWithRequiredField) Validate() error {
	validatorWithRequiredFieldCalls++
	return nil
}

func TestValidator_SkippedOnFieldErrors(t *testing.T) {
	validatorWithRequiredFieldCalls = 0
	loader := New(WithSource(Map(map[string]string{}))) // X unset -> required error
	var c validatorWithRequiredField
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error from missing required field")
	}
	if validatorWithRequiredFieldCalls != 0 {
		t.Errorf("Validate called %d times despite field errors; should have been skipped", validatorWithRequiredFieldCalls)
	}
}

// --- no validator at all ---

type validatorNone struct {
	X int `env:"X,default=1"`
}

func TestValidator_NoneIsHarmless(t *testing.T) {
	// A struct that does not implement Validator should load normally.
	loader := New(WithSource(Map(map[string]string{})))
	var c validatorNone
	if err := loader.Load(&c); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

// --- ValidationError unwraps to user error ---

var errCustomValidation = errors.New("user-defined validation error")

type validatorCustomError struct {
	X int `env:"X,default=1"`
}

func (*validatorCustomError) Validate() error { return errCustomValidation }

func TestValidator_UnwrapsToCause(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{})))
	var c validatorCustomError
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	// errors.Is should reach the original cause.
	if !errors.Is(err, errCustomValidation) {
		t.Errorf("err does not unwrap to user error: %v", err)
	}
	// And should also match the ValidationError sentinel.
	if !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

// --- value-receiver Validate is not detected (pointer receiver only) ---

type validatorValueReceiver struct {
	X int `env:"X,default=1"`
}

// Note: value receiver, not pointer. The loader looks for Validate on
// the addressable target's pointer type, so value-receiver methods are
// also matched (Go promotes value methods to pointer types).
func (validatorValueReceiver) Validate() error {
	return errors.New("value-receiver fired")
}

func TestValidator_ValueReceiverAlsoMatches(t *testing.T) {
	loader := New(WithSource(Map(map[string]string{})))
	var c validatorValueReceiver
	err := loader.Load(&c)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("value-receiver Validate not detected")
	}
}
