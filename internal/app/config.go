package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
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
}

// LookupEnv reads a runtime setting.
//
// Injecting this function keeps configuration loading independent of the process
// environment and allows tests to supply isolated settings.
type LookupEnv func(string) (string, bool)

// LoadConfig reads and validates runtime settings.
//
// Unset variables receive defaults. Explicitly empty variables are rejected,
// preventing deployment mistakes from silently falling back to defaults.
func LoadConfig(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("environment lookup is required")
	}

	var cfg Config
	var err error

	cfg.ListenAddress, err = readSetting(
		lookup, "OPAMP_LISTEN_ADDRESS", "127.0.0.1:4320",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.TLSCertFile = optionalSetting(lookup, "OPAMP_TLS_CERT_FILE")
	cfg.TLSKeyFile = optionalSetting(lookup, "OPAMP_TLS_KEY_FILE")
	messageLimit, err := readSetting(
		lookup, "OPAMP_MAX_MESSAGE_BYTES", "1048576",
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

	cfg.AuthTimeout, err = readDuration(
		lookup, "OPAMP_AUTH_TIMEOUT", "5s",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.ShutdownTimeout, err = readDuration(
		lookup, "OPAMP_SHUTDOWN_TIMEOUT", "10s",
	)
	if err != nil {
		return Config{}, err
	}

	cfg.OpAMPPath, err = readSetting(
		lookup, "OPAMP_PATH", "/v1/opamp",
	)
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

	return nil
}

// readSetting distinguishes an absent setting from an explicitly empty value.
func readSetting(lookup LookupEnv, key, fallback string) (string, error) {
	value, exists := lookup(key)
	if !exists {
		return fallback, nil
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}

	return value, nil
}

// optionalSetting returns an empty string when an optional setting is absent.
func optionalSetting(lookup LookupEnv, key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

// readDuration parses Go duration syntax, such as "500ms" or "5s".
func readDuration(
	lookup LookupEnv,
	key string,
	fallback string,
) (time.Duration, error) {
	value, err := readSetting(lookup, key, fallback)
	if err != nil {
		return 0, err
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}

	return duration, nil
}
