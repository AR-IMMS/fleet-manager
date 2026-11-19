package config

import (
	"fmt"
	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
	"strings"
)

// LoggingConfig contains environment-controlled application logging settings.
type LoggingConfig struct {
	Level   string
	Format  string
	Service string
}

// loadLoggingConfig reads logging settings without creating a logger.
func loadLoggingConfig(reader env.Reader) (LoggingConfig, error) {
	var cfg LoggingConfig
	var err error

	cfg.Level, err = reader.Setting("FLEET_LOG_LEVEL", "info")
	if err != nil {
		return LoggingConfig{}, err
	}

	cfg.Format, err = reader.Setting("FLEET_LOG_FORMAT", "json")
	if err != nil {
		return LoggingConfig{}, err
	}

	cfg.Service, err = reader.Setting(
		"FLEET_LOG_SERVICE", "fleet-manager",
	)
	if err != nil {
		return LoggingConfig{}, err
	}

	cfg.Level = strings.ToLower(cfg.Level)
	cfg.Format = strings.ToLower(cfg.Format)

	if err := cfg.Validate(); err != nil {
		return LoggingConfig{}, err
	}

	return cfg, nil
}

// Validate rejects invalid logging configuration before dependencies start.
func (c LoggingConfig) Validate() error {
	switch c.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("FLEET_LOG_LEVEL must be debug, info, warn, or error")
	}

	if c.Format != "json" && c.Format != "text" {
		return fmt.Errorf("FLEET_LOG_FORMAT must be json or text")
	}

	if strings.TrimSpace(c.Service) == "" {
		return fmt.Errorf("FLEET_LOG_SERVICE must not be empty")
	}

	return nil
}
