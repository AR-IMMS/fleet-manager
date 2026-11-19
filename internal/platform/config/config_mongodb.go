package config

import (
	"fmt"
	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
	"strings"
	"time"
)

// MongoConfig contains Fleet-owned database connection settings.
//
// URI identifies the MongoDB deployment and authentication database.
// Database selects the application's logical database independently.
//
// This value contains sensitive connection information and must not be
// serialized into logs, diagnostics, or configuration responses.
type MongoConfig struct {
	URI              string
	Database         string
	AppName          string
	ConnectTimeout   time.Duration
	OperationTimeout time.Duration
}

// loadMongoConfig reads MongoDB settings without opening a connection.
//
// Missing settings receive local-development defaults. Explicitly empty
// values are rejected by the shared setting readers.
func loadMongoConfig(reader env.Reader) (MongoConfig, error) {
	var cfg MongoConfig
	var err error

	cfg.URI, err = reader.Setting(
		"FLEET_MONGODB_URI",
		"mongodb://127.0.0.1:27017",
	)
	if err != nil {
		return MongoConfig{}, fmt.Errorf("reading MongoDB URI: %w", err)
	}

	cfg.Database, err = reader.Setting(
		"FLEET_MONGODB_DATABASE",
		"ar_imms_fleet",
	)
	if err != nil {
		return MongoConfig{}, fmt.Errorf("reading MongoDB database: %w", err)
	}

	cfg.AppName, err = reader.Setting(
		"FLEET_MONGODB_APP_NAME",
		"ar-imms-fleet-manager",
	)
	if err != nil {
		return MongoConfig{}, fmt.Errorf("reading MongoDB app name: %w", err)
	}

	cfg.ConnectTimeout, err = reader.Duration(
		"FLEET_MONGODB_CONNECT_TIMEOUT",
		"5s",
	)
	if err != nil {
		return MongoConfig{}, fmt.Errorf("reading MongoDB connect timeout: %w", err)
	}

	cfg.OperationTimeout, err = reader.Duration(
		"FLEET_MONGODB_OPERATION_TIMEOUT",
		"5s",
	)
	if err != nil {
		return MongoConfig{}, fmt.Errorf("reading MongoDB operation timeout: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return MongoConfig{}, fmt.Errorf("validating MongoDB config: %w", err)
	}

	return cfg, nil
}

// Validate rejects invalid local settings before dependencies start.
//
// URI parsing and driver-specific option validation belong to the MongoDB
// adapter. Errors here intentionally exclude the potentially secret URI.
func (c MongoConfig) Validate() error {
	if strings.TrimSpace(c.URI) == "" {
		return fmt.Errorf("MongoDB URI must not be empty")
	}

	if !strings.HasPrefix(c.URI, "mongodb://") &&
		!strings.HasPrefix(c.URI, "mongodb+srv://") {
		return fmt.Errorf("MongoDB URI must use mongodb or mongodb+srv")
	}

	if c.Database == "" ||
		strings.ContainsAny(c.Database, "/\\ .\"$*<>:|?\x00") ||
		len(c.Database) >= 64 {
		return fmt.Errorf(
			"MongoDB database name must be valid and shorter than 64 bytes",
		)
	}

	switch strings.ToLower(c.Database) {
	case "admin", "config", "local":
		return fmt.Errorf("fleet must use an application database")
	}

	if strings.TrimSpace(c.AppName) == "" {
		return fmt.Errorf("MongoDB app name must not be empty")
	}

	if c.ConnectTimeout <= 0 {
		return fmt.Errorf("MongoDB connection timeout must be positive")
	}

	if c.OperationTimeout <= 0 {
		return fmt.Errorf("MongoDB operation timeout must be positive")
	}

	return nil
}
