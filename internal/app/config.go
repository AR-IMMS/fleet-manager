// Package app provides the runtime configuration aggregate used by composition.
package app

import platformconfig "github.com/AR-IMMS/fleet-manager/internal/platform/config"

// Config aliases the platform-loaded configuration for app wiring.
type Config = platformconfig.Config

// OpAMPConfig contains protocol listener and authentication settings.
type OpAMPConfig = platformconfig.OpAMPConfig

// MongoConfig contains MongoDB connection settings.
type MongoConfig = platformconfig.MongoConfig

// LoggingConfig contains structured logging settings.
type LoggingConfig = platformconfig.LoggingConfig

// HTTPConfig contains Fleet REST listener settings.
type HTTPConfig = platformconfig.HTTPConfig

// EnrollmentConfig contains enrollment route and secret escrow settings.
type EnrollmentConfig = platformconfig.EnrollmentConfig

// LookupEnv reads a runtime setting.
type LookupEnv = platformconfig.LookupEnv

// LoadConfig loads and validates runtime settings for the composition root.
func LoadConfig(lookup LookupEnv) (Config, error) {
	return platformconfig.LoadConfig(lookup)
}
