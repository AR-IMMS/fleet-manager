package config

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
)

// HTTPConfig contains settings for the Fleet REST and health listener.
//
// This baseline allows plaintext only on an explicit loopback IP.
// OpAMP uses a separate listener and its own settings.
type HTTPConfig struct {
	ListenAddress     string        `json:"listen_address" yaml:"listen_address"`
	TLSCertFile       string        `json:"tls_cert_file" yaml:"tls_cert_file"`
	TLSKeyFile        string        `json:"tls_key_file" yaml:"tls_key_file"`
	ClientCAFile      string        `json:"client_ca_file" yaml:"client_ca_file"`
	APIDocsEnabled    bool          `json:"api_docs_enabled" yaml:"api_docs_enabled"`
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout      time.Duration `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout       time.Duration `json:"idle_timeout" yaml:"idle_timeout"`
	ReadinessTimeout  time.Duration `json:"readiness_timeout" yaml:"readiness_timeout"`
}

// loadHTTPConfig reads HTTP settings without starting a listener.
//
// Unset variables receive defaults; explicitly empty values are rejected
// by the environment reader.
func loadHTTPConfig(reader env.Reader) (HTTPConfig, error) {
	var cfg HTTPConfig
	var err error

	cfg.ListenAddress, err = reader.Setting(
		"FLEET_HTTP_LISTEN_ADDRESS",
		"127.0.0.1:8081",
	)
	if err != nil {
		return HTTPConfig{}, err
	}
	cfg.TLSCertFile = reader.Optional("FLEET_HTTP_TLS_CERT_FILE")
	cfg.TLSKeyFile = reader.Optional("FLEET_HTTP_TLS_KEY_FILE")
	cfg.ClientCAFile = reader.Optional("FLEET_HTTP_CLIENT_CA_FILE")
	docsEnabled, err := reader.Setting("FLEET_API_DOCS_ENABLED", "false")
	if err != nil {
		return HTTPConfig{}, err
	}
	cfg.APIDocsEnabled, err = strconv.ParseBool(docsEnabled)
	if err != nil {
		return HTTPConfig{}, fmt.Errorf("FLEET_API_DOCS_ENABLED must be a boolean")
	}

	cfg.ReadHeaderTimeout, err = reader.Duration(
		"FLEET_HTTP_READ_HEADER_TIMEOUT",
		"5s",
	)
	if err != nil {
		return HTTPConfig{}, err
	}

	cfg.ReadTimeout, err = reader.Duration(
		"FLEET_HTTP_READ_TIMEOUT",
		"10s",
	)
	if err != nil {
		return HTTPConfig{}, err
	}

	cfg.WriteTimeout, err = reader.Duration(
		"FLEET_HTTP_WRITE_TIMEOUT",
		"10s",
	)
	if err != nil {
		return HTTPConfig{}, err
	}

	cfg.IdleTimeout, err = reader.Duration(
		"FLEET_HTTP_IDLE_TIMEOUT",
		"60s",
	)
	if err != nil {
		return HTTPConfig{}, err
	}

	cfg.ReadinessTimeout, err = reader.Duration(
		"FLEET_HTTP_READINESS_TIMEOUT",
		"2s",
	)
	if err != nil {
		return HTTPConfig{}, err
	}

	if err := cfg.Validate(); err != nil {
		return HTTPConfig{}, err
	}

	return cfg, nil
}

// Validate checks listener safety and bounded HTTP timeouts.
//
// Port zero is supported for ephemeral integration-test listeners.
// The readiness deadline must fit within both HTTP read and write budgets.
func (c HTTPConfig) Validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return fmt.Errorf("invalid http listen address")
	}

	ip := net.ParseIP(host)
	hasCert := c.TLSCertFile != ""
	hasKey := c.TLSKeyFile != ""
	if hasCert != hasKey {
		return fmt.Errorf("http TLS certificate and key must be configured together")
	}
	if !hasCert && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("http listener must use an explicit loopback ip without TLS")
	}
	if c.ClientCAFile != "" && !hasCert {
		return fmt.Errorf("http client CA requires TLS")
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("http port must be between 0 and 65535")
	}

	if c.ReadHeaderTimeout <= 0 ||
		c.ReadTimeout <= 0 ||
		c.WriteTimeout <= 0 ||
		c.IdleTimeout <= 0 ||
		c.ReadinessTimeout <= 0 {
		return fmt.Errorf("http timeouts must be positive")
	}

	if c.ReadinessTimeout >= c.ReadTimeout ||
		c.ReadinessTimeout >= c.WriteTimeout {
		return fmt.Errorf(
			"readiness timeout must be shorter than http read and write timeouts",
		)
	}

	return nil
}
