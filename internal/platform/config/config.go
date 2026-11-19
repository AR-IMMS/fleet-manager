package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
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
	ListenAddress   string        `json:"listen_address" yaml:"listen_address"`
	OpAMPPath       string        `json:"opamp_path" yaml:"opamp_path"`
	TLSCertFile     string        `json:"tls_cert_file" yaml:"tls_cert_file"`
	TLSKeyFile      string        `json:"tls_key_file" yaml:"tls_key_file"`
	MaxMessageBytes int64         `json:"max_message_bytes" yaml:"max_message_bytes"`
	AuthTimeout     time.Duration `json:"auth_timeout" yaml:"auth_timeout"`
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

	cfg.ListenAddress, err = env.Setting(
		"OPAMP_LISTEN_ADDRESS", "127.0.0.1:4320",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.TLSCertFile = env.Optional("OPAMP_TLS_CERT_FILE")
	cfg.TLSKeyFile = env.Optional("OPAMP_TLS_KEY_FILE")

	messageLimit, err := env.Setting(
		"OPAMP_MAX_MESSAGE_BYTES", "1048576",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.MaxMessageBytes, err = strconv.ParseInt(messageLimit, 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf(
			"OPAMP_MAX_MESSAGE_BYTES must be an integer: %w", err,
		)
	}

	cfg.AuthTimeout, err = env.Duration(
		"OPAMP_AUTH_TIMEOUT", "5s",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.ShutdownTimeout, err = env.Duration(
		"FLEET_SHUTDOWN_TIMEOUT", "10s",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.OpAMPPath, err = env.Setting(
		"OPAMP_PATH", "/v1/opamp",
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
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("listen port must be between 0 and 65535")
	}

	// Port zero is intentionally supported for ephemeral integration-test listeners.
	if c.OpAMPPath == "" || !strings.HasPrefix(c.OpAMPPath, "/") {
		return fmt.Errorf("OpAMP path must start with '/'")
	}

	if strings.ContainsAny(c.OpAMPPath, "?# \t\r\n") {
		return fmt.Errorf("OpAMP path must not contain query, fragment, or whitespace")
	}

	hasCert := c.TLSCertFile != ""
	hasKey := c.TLSKeyFile != ""

	if hasCert != hasKey {
		return fmt.Errorf("TLS certificate and key must be configured together")
	}

	if !hasCert {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("TLS is required for non-loopback listeners")
		}
	}

	if c.MaxMessageBytes <= 0 {
		return fmt.Errorf("maximum message size must be positive")
	}

	if c.AuthTimeout <= 0 {
		return fmt.Errorf("authentication timeout must be positive")
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
