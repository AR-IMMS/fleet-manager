package env

import (
	"fmt"
	"strings"
	"time"
)

// LookupEnv reads a runtime setting.
//
// Injection allows tests to provide isolated configuration without mutating
// the process environment.
type LookupEnv func(string) (string, bool)

// envReader reads settings through an injected environment lookup.
//
// It owns parsing and absent-versus-empty semantics. Configuration defaults
// and validation remain with the configuration section that owns each setting.
type Reader struct {
	lookup LookupEnv
}

// newEnvReader rejects missing lookup wiring before configuration is read.
func NewReader(lookup LookupEnv) (Reader, error) {
	if lookup == nil {
		return Reader{}, fmt.Errorf("environment lookup is required")
	}

	return Reader{lookup: lookup}, nil
}

// setting returns the fallback only when the variable is absent.
//
// Explicitly empty or whitespace-only values are rejected.
func (r Reader) Setting(key, fallback string) (string, error) {
	value, exists := r.lookup(key)
	if !exists {
		return fallback, nil
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}

	return value, nil
}

// optional returns an empty string for absent or explicitly empty settings.
func (r Reader) Optional(key string) string {
	value, _ := r.lookup(key)
	return strings.TrimSpace(value)
}

// duration parses Go duration syntax without exposing the supplied value.
//
// Positivity and other semantic constraints belong to configuration validation.
func (r Reader) Duration(
	key string,
	fallback string,
) (time.Duration, error) {
	value, err := r.Setting(key, fallback)
	if err != nil {
		return 0, err
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", key)
	}

	return duration, nil
}
