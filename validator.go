package env

import "context"

// Validator is implemented by struct types that need cross-field
// validation after loading. The loader detects it via interface
// assertion on the target's pointer receiver and calls Validate once,
// after every field has been populated. If per-field errors exist
// Validate is skipped, since the struct is in a partial state. A
// non-nil return is wrapped in a [*ValidationError] and added to the
// [*MultiError].
//
//	type Config struct {
//	    DBHost string `env:"DB_HOST"`
//	    DBUser string `env:"DB_USER"`
//	}
//
//	func (c *Config) Validate() error {
//	    if c.DBHost != "" && c.DBUser == "" {
//	        return fmt.Errorf("DB_USER is required when DB_HOST is set")
//	    }
//	    return nil
//	}
type Validator interface {
	Validate() error
}

// ValidatorContext is like [Validator] but receives the context passed
// to [Loader.LoadContext] (or context.Background() for [Loader.Load]).
// The loader uses whichever of ValidatorContext / Validator the target
// struct implements — it can only be one (Go method names cannot be
// overloaded).
type ValidatorContext interface {
	Validate(ctx context.Context) error
}
