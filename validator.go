package env

// Validator is implemented by struct types that need cross-field validation
// after loading. It is detected via interface assertion on the target's
// pointer-receiver type.
//
// The loader calls Validate once after every field has been populated and
// after every per-field error has been aggregated. If field-level errors
// exist, Validate is NOT called (the struct is in a partial state and
// running Validate against it would produce noise that's likely to
// confuse rather than help).
//
// On a successful load (no field errors), a non-nil Validate return is
// wrapped in a [*ValidationError] and added to the [*MultiError].
//
// Cross-field rules go here; per-field rules go in the field's tag
// (required, notEmpty, default=) or in a custom decoder.
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
