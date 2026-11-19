package config

import (
	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
	"testing"
	"time"
)

func testEnvReader(lookup env.LookupEnv) env.Reader {
	reader, _ := env.NewReader(lookup)
	return reader
}

// TestLoadMongoConfigDefaults verifies isolated local-development defaults.
func TestLoadMongoConfigDefaults(t *testing.T) {
	cfg, err := loadMongoConfig(testEnvReader(func(string) (string, bool) {
		return "", false
	}))
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}

	if cfg.URI != "mongodb://127.0.0.1:27017" {
		t.Fatal("unexpected default MongoDB URI")
	}

	if cfg.Database != "ar_imms_fleet" {
		t.Fatalf("unexpected database: %q", cfg.Database)
	}

	if cfg.AppName != "ar-imms-fleet-manager" {
		t.Fatalf("unexpected app name: %q", cfg.AppName)
	}

	if cfg.ConnectTimeout != 5*time.Second ||
		cfg.OperationTimeout != 5*time.Second {
		t.Fatal("unexpected default timeouts")
	}
}

// TestLoadMongoConfigRejectsExplicitEmptyURI prevents silent fallback.
func TestLoadMongoConfigRejectsExplicitEmptyURI(t *testing.T) {
	_, err := loadMongoConfig(testEnvReader(func(key string) (string, bool) {
		if key == "FLEET_MONGODB_URI" {
			return " ", true
		}

		return "", false
	}))
	if err == nil {
		t.Fatal("expected explicitly empty URI to fail")
	}
}

// TestLoadMongoConfigRejectsExplicitEmptyAppName prevents silent fallback.
func TestLoadMongoConfigRejectsExplicitEmptyAppName(t *testing.T) {
	_, err := loadMongoConfig(testEnvReader(func(key string) (string, bool) {
		if key == "FLEET_MONGODB_APP_NAME" {
			return " ", true
		}

		return "", false
	}))
	if err == nil {
		t.Fatal("expected explicitly empty app name to fail")
	}
}

// TestMongoConfigRejectsInvalidDatabase protects application database scope.
func TestMongoConfigRejectsInvalidDatabase(t *testing.T) {
	for _, name := range []string{
		"", "admin", "config", "local", "fleet/db", "fleet db",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := MongoConfig{
				URI:              "mongodb://127.0.0.1:27017",
				Database:         name,
				AppName:          "ar-imms-fleet-manager",
				ConnectTimeout:   time.Second,
				OperationTimeout: time.Second,
			}

			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid database to fail")
			}
		})
	}
}

// TestMongoConfigRejectsEmptyAppName ensures a non-blank app name is set.
func TestMongoConfigRejectsEmptyAppName(t *testing.T) {
	cfg := MongoConfig{
		URI:              "mongodb://127.0.0.1:27017",
		Database:         "ar_imms_fleet",
		AppName:          "   ",
		ConnectTimeout:   time.Second,
		OperationTimeout: time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected empty app name to fail")
	}
}
