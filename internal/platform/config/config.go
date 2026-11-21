package config

import (
	"fmt"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
)

// Config contains runtime settings.
//
// JSON and YAML use the same field names. Struct tags define serialization names;
// they do not load environment variables or validate configuration.
//
// Duration values use Go's time.Duration representation. Human-readable strings
// such as "5s" require explicit parsing, as performed by LoadConfig.
type Config struct {
	OpAMPConfig     `yaml:",inline"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout" yaml:"shutdown_timeout"`
	Mongo           MongoConfig   `json:"-" yaml:"-"`
	Logging         LoggingConfig `json:"-" yaml:"-"`
	HTTP            HTTPConfig    `json:"http" yaml:"http"`
}

// LookupEnv is the environment lookup accepted by the configuration loader.
type LookupEnv = env.LookupEnv

// LoadConfig reads and validates runtime settings.
//
// Unset variables receive defaults. Explicitly empty variables are rejected,
// preventing deployment mistakes from silently falling back to defaults.
func LoadConfig(lookup env.LookupEnv) (Config, error) {
	env, err := env.NewReader(lookup)
	if err != nil {
		return Config{}, err
	}

	var cfg Config

	cfg.OpAMPConfig, err = loadOpAMPConfig(env)
	if err != nil {
		return Config{}, err
	}

	cfg.ShutdownTimeout, err = env.Duration(
		"FLEET_SHUTDOWN_TIMEOUT", "10s",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.Mongo, err = loadMongoConfig(env)
	if err != nil {
		return Config{}, err
	}

	cfg.Logging, err = loadLoggingConfig(env)
	if err != nil {
		return Config{}, err
	}

	cfg.HTTP, err = loadHTTPConfig(env)
	if err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks settings before any listener or background worker starts.
//
// Plaintext is permitted only on an explicit loopback IP. Hostnames are not
// resolved here because their resolution can change between validation and bind.
func (c Config) Validate() error {
	if err := c.OpAMPConfig.Validate(); err != nil {
		return err
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be positive")
	}

	if err := c.Mongo.Validate(); err != nil {
		return err
	}

	if err := c.Logging.Validate(); err != nil {
		return err
	}

	return nil
}
