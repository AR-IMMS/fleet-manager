package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
)

// EnrollmentConfig controls the provisional Core and Agent enrollment routes.
// The feature is disabled unless explicitly enabled by deployment config.
type EnrollmentConfig struct {
	Enabled          bool          `json:"enabled" yaml:"enabled"`
	EncryptionKey    []byte        `json:"-" yaml:"-"`
	DefaultInviteTTL time.Duration `json:"default_invitation_ttl" yaml:"default_invitation_ttl"`
	MaximumInviteTTL time.Duration `json:"maximum_invitation_ttl" yaml:"maximum_invitation_ttl"`
	RecoveryTTL      time.Duration `json:"recovery_ttl" yaml:"recovery_ttl"`
	OpAMPEndpoint    string        `json:"opamp_endpoint" yaml:"opamp_endpoint"`
	CoreClientCN     string        `json:"core_client_cn" yaml:"core_client_cn"`
}

func loadEnrollmentConfig(reader env.Reader) (EnrollmentConfig, error) {
	var cfg EnrollmentConfig
	enabled, err := reader.Setting("FLEET_ENROLLMENT_ENABLED", "false")
	if err != nil {
		return EnrollmentConfig{}, err
	}
	cfg.Enabled, err = strconv.ParseBool(enabled)
	if err != nil {
		return EnrollmentConfig{}, fmt.Errorf("FLEET_ENROLLMENT_ENABLED must be a boolean")
	}
	cfg.DefaultInviteTTL, err = reader.Duration("FLEET_ENROLLMENT_DEFAULT_INVITATION_TTL", "30m")
	if err != nil {
		return EnrollmentConfig{}, err
	}
	cfg.MaximumInviteTTL, err = reader.Duration("FLEET_ENROLLMENT_MAXIMUM_INVITATION_TTL", "24h")
	if err != nil {
		return EnrollmentConfig{}, err
	}
	cfg.RecoveryTTL, err = reader.Duration("FLEET_ENROLLMENT_RECOVERY_TTL", "24h")
	if err != nil {
		return EnrollmentConfig{}, err
	}
	cfg.OpAMPEndpoint = reader.Optional("FLEET_ENROLLMENT_OPAMP_ENDPOINT")
	cfg.CoreClientCN = reader.Optional("FLEET_ENROLLMENT_CORE_CLIENT_CN")
	encodedKey := reader.Optional("FLEET_ENROLLMENT_ENCRYPTION_KEY")
	if encodedKey != "" {
		cfg.EncryptionKey, err = base64.RawStdEncoding.DecodeString(encodedKey)
		if err != nil {
			cfg.EncryptionKey, err = base64.StdEncoding.DecodeString(encodedKey)
		}
		if err != nil || len(cfg.EncryptionKey) != 32 {
			return EnrollmentConfig{}, fmt.Errorf("FLEET_ENROLLMENT_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
		}
	}
	if err := cfg.Validate(); err != nil {
		return EnrollmentConfig{}, err
	}
	return cfg, nil
}

// Validate rejects incomplete activation and preserves disabled-by-default behavior.
func (c EnrollmentConfig) Validate() error {
	if c.DefaultInviteTTL <= 0 || c.MaximumInviteTTL < c.DefaultInviteTTL || c.RecoveryTTL <= 0 || c.RecoveryTTL > 7*24*time.Hour {
		return fmt.Errorf("enrollment invitation and recovery lifetime bounds are invalid")
	}
	if !c.Enabled {
		return nil
	}
	if len(c.EncryptionKey) != 32 {
		return fmt.Errorf("enrollment requires a 32-byte encryption key")
	}
	if strings.TrimSpace(c.CoreClientCN) == "" || strings.TrimSpace(c.CoreClientCN) != c.CoreClientCN {
		return fmt.Errorf("enrollment requires an exact Core client certificate common name")
	}
	endpoint, err := url.Parse(c.OpAMPEndpoint)
	if err != nil || endpoint.Scheme != "wss" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("enrollment requires a WSS OpAMP endpoint")
	}
	return nil
}
