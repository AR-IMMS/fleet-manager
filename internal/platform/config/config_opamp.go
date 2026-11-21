package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
)

// OpAMPConfig contains protocol listener settings; environment names and wire fields are preserved.
type OpAMPConfig struct {
	Enabled         bool          `json:"enabled" yaml:"enabled"`
	ListenAddress   string        `json:"listen_address" yaml:"listen_address"`
	OpAMPPath       string        `json:"opamp_path" yaml:"opamp_path"`
	TLSCertFile     string        `json:"tls_cert_file" yaml:"tls_cert_file"`
	TLSKeyFile      string        `json:"tls_key_file" yaml:"tls_key_file"`
	MaxMessageBytes int64         `json:"max_message_bytes" yaml:"max_message_bytes"`
	AuthTimeout     time.Duration `json:"auth_timeout" yaml:"auth_timeout"`
}

func loadOpAMPConfig(reader env.Reader) (OpAMPConfig, error) {
	var cfg OpAMPConfig
	var err error
	enabled, err := reader.Setting("OPAMP_ENABLED", "false")
	if err != nil {
		return OpAMPConfig{}, err
	}
	cfg.Enabled, err = strconv.ParseBool(enabled)
	if err != nil {
		return OpAMPConfig{}, fmt.Errorf("OPAMP_ENABLED must be a boolean")
	}

	cfg.ListenAddress, err = reader.Setting(
		"OPAMP_LISTEN_ADDRESS", "127.0.0.1:4320",
	)
	if err != nil {
		return OpAMPConfig{}, err
	}

	cfg.TLSCertFile = reader.Optional("OPAMP_TLS_CERT_FILE")
	cfg.TLSKeyFile = reader.Optional("OPAMP_TLS_KEY_FILE")

	messageLimit, err := reader.Setting(
		"OPAMP_MAX_MESSAGE_BYTES", "1048576",
	)
	if err != nil {
		return OpAMPConfig{}, err
	}

	cfg.MaxMessageBytes, err = strconv.ParseInt(messageLimit, 10, 64)
	if err != nil {
		return OpAMPConfig{}, fmt.Errorf(
			"OPAMP_MAX_MESSAGE_BYTES must be an integer: %w", err,
		)
	}

	cfg.AuthTimeout, err = reader.Duration(
		"OPAMP_AUTH_TIMEOUT", "5s",
	)
	if err != nil {
		return OpAMPConfig{}, err
	}

	cfg.OpAMPPath, err = reader.Setting(
		"OPAMP_PATH", "/v1/opamp",
	)
	if err != nil {
		return OpAMPConfig{}, err
	}

	if err := cfg.Validate(); err != nil {
		return OpAMPConfig{}, err
	}
	return cfg, nil
}

// Validate permits plaintext only on explicit loopback IPs and requires bounded messages and authentication.
func (c OpAMPConfig) Validate() error {
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

	return nil
}
