package app

import (
	"testing"
	"time"
)

// lookupSettings creates an isolated environment reader.
//
// Tests never modify the process environment, so they can run concurrently
// without leaking configuration into other tests.
func lookupSettings(settings map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, exists := settings[key]
		return value, exists
	}
}

// TestLoadConfigDefaults verifies the documented local-development defaults.
func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(lookupSettings(nil))
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:4320" {
		t.Errorf("unexpected listen address: %q", cfg.ListenAddress)
	}

	if cfg.OpAMPPath != "/v1/opamp" {
		t.Errorf("unexpected OpAMP path: %q", cfg.OpAMPPath)
	}

	if cfg.MaxMessageBytes != 1<<20 {
		t.Errorf("unexpected message limit: %d", cfg.MaxMessageBytes)
	}

	if cfg.AuthTimeout != 5*time.Second {
		t.Errorf("unexpected authentication timeout: %s", cfg.AuthTimeout)
	}

	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("unexpected shutdown timeout: %s", cfg.ShutdownTimeout)
	}
}

// TestLoadConfigOverrides verifies that explicitly supplied settings take effect.
func TestLoadConfigOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(lookupSettings(map[string]string{
		"OPAMP_LISTEN_ADDRESS":    "[::1]:0",
		"OPAMP_PATH":              "/management/opamp",
		"OPAMP_MAX_MESSAGE_BYTES": "2097152",
		"OPAMP_AUTH_TIMEOUT":      "750ms",
		"OPAMP_SHUTDOWN_TIMEOUT":  "20s",
	}))
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddress != "[::1]:0" ||
		cfg.OpAMPPath != "/management/opamp" ||
		cfg.MaxMessageBytes != 2<<20 ||
		cfg.AuthTimeout != 750*time.Millisecond ||
		cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("settings were not applied: %+v", cfg)
	}
}

// TestLoadConfigRejectsInvalidSettings verifies fail-fast startup behavior.
func TestLoadConfigRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]string
	}{
		{
			name:     "explicitly empty setting",
			settings: map[string]string{"OPAMP_LISTEN_ADDRESS": ""},
		},
		{
			name:     "malformed address",
			settings: map[string]string{"OPAMP_LISTEN_ADDRESS": "127.0.0.1"},
		},
		{
			name:     "invalid port",
			settings: map[string]string{"OPAMP_LISTEN_ADDRESS": "127.0.0.1:65536"},
		},
		{
			name:     "public plaintext listener",
			settings: map[string]string{"OPAMP_LISTEN_ADDRESS": "0.0.0.0:4320"},
		},
		{
			name:     "hostname plaintext listener",
			settings: map[string]string{"OPAMP_LISTEN_ADDRESS": "localhost:4320"},
		},
		{
			name:     "certificate without key",
			settings: map[string]string{"OPAMP_TLS_CERT_FILE": "server.crt"},
		},
		{
			name:     "key without certificate",
			settings: map[string]string{"OPAMP_TLS_KEY_FILE": "server.key"},
		},
		{
			name:     "relative path",
			settings: map[string]string{"OPAMP_PATH": "v1/opamp"},
		},
		{
			name:     "path containing query",
			settings: map[string]string{"OPAMP_PATH": "/v1/opamp?debug=true"},
		},
		{
			name:     "invalid message limit",
			settings: map[string]string{"OPAMP_MAX_MESSAGE_BYTES": "large"},
		},
		{
			name:     "disabled message limit",
			settings: map[string]string{"OPAMP_MAX_MESSAGE_BYTES": "0"},
		},
		{
			name:     "invalid duration",
			settings: map[string]string{"OPAMP_AUTH_TIMEOUT": "five seconds"},
		},
		{
			name:     "non-positive authentication timeout",
			settings: map[string]string{"OPAMP_AUTH_TIMEOUT": "-1s"},
		},
		{
			name:     "non-positive shutdown timeout",
			settings: map[string]string{"OPAMP_SHUTDOWN_TIMEOUT": "0s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := LoadConfig(lookupSettings(tt.settings)); err == nil {
				t.Fatal("expected invalid configuration to be rejected")
			}
		})
	}
}

// TestLoadConfigAcceptsTLSSettings checks configuration-level validation only.
//
// Certificate loading and cryptographic validation belong to the transport
// startup tests; specifying file paths does not prove that TLS can start.
func TestLoadConfigAcceptsTLSSettings(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(lookupSettings(map[string]string{
		"OPAMP_LISTEN_ADDRESS": "0.0.0.0:4320",
		"OPAMP_TLS_CERT_FILE":  "server.crt",
		"OPAMP_TLS_KEY_FILE":   "server.key",
	}))
	if err != nil {
		t.Fatalf("expected paired TLS settings to be accepted: %v", err)
	}
}

func TestLoadConfigRejectsMissingLookup(t *testing.T) {
	t.Parallel()

	if _, err := LoadConfig(nil); err == nil {
		t.Fatal("expected missing environment reader to be rejected")
	}
}
