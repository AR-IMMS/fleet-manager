package app

import (
	"strings"
	"testing"
	"time"
)

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig(mapLookup(map[string]string{}))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:4320" {
		t.Fatalf("unexpected ListenAddress: %q", cfg.ListenAddress)
	}
	if cfg.OpAMPPath != "/v1/opamp" {
		t.Fatalf("unexpected OpAMPPath: %q", cfg.OpAMPPath)
	}
	if cfg.MaxMessageBytes != 1048576 {
		t.Fatalf("unexpected MaxMessageBytes: %d", cfg.MaxMessageBytes)
	}
	if cfg.AuthTimeout != 5*time.Second {
		t.Fatalf("unexpected AuthTimeout: %v", cfg.AuthTimeout)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected ShutdownTimeout: %v", cfg.ShutdownTimeout)
	}
	if cfg.Mongo.Database != "ar_imms_fleet" {
		t.Fatalf("unexpected Mongo.Database: %q", cfg.Mongo.Database)
	}
	if cfg.Enrollment.Enabled {
		t.Fatal("enrollment must default to disabled")
	}
}

func TestLoadConfig_NilLookup(t *testing.T) {
	if _, err := LoadConfig(nil); err == nil {
		t.Fatal("expected error when lookup is nil")
	}
}

func TestLoadConfig_RejectsEmptyRequiredSetting(t *testing.T) {
	_, err := LoadConfig(mapLookup(map[string]string{
		"OPAMP_PATH": "   ",
	}))
	if err == nil {
		t.Fatal("expected error for whitespace/empty required setting")
	}
	if !strings.Contains(err.Error(), "OPAMP_PATH must not be empty") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_ValidationRules(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name: "non-loopback without TLS is rejected",
			env: map[string]string{
				"OPAMP_LISTEN_ADDRESS": "0.0.0.0:4320",
			},
			wantErr: "TLS is required for non-loopback listeners",
		},
		{
			name: "tls cert without key is rejected",
			env: map[string]string{
				"OPAMP_TLS_CERT_FILE": "/path/cert.pem",
			},
			wantErr: "TLS certificate and key must be configured together",
		},
		{
			name: "invalid opamp path is rejected",
			env: map[string]string{
				"OPAMP_PATH": "v1/opamp",
			},
			wantErr: "OpAMP path must start with '/'",
		},
		{
			name: "reserved mongo database is rejected",
			env: map[string]string{
				"FLEET_MONGODB_DATABASE": "admin",
			},
			wantErr: "fleet must use an application database",
		},
		{
			name: "non-positive mongo timeout is rejected",
			env: map[string]string{
				"FLEET_MONGODB_CONNECT_TIMEOUT": "0s",
			},
			wantErr: "MongoDB connection timeout must be positive",
		},
		{
			name: "invalid logging level is rejected",
			env: map[string]string{
				"FLEET_LOG_LEVEL": "trace",
			},
			wantErr: "FLEET_LOG_LEVEL must be",
		},
		{
			name: "invalid logging format is rejected",
			env: map[string]string{
				"FLEET_LOG_FORMAT": "xml",
			},
			wantErr: "FLEET_LOG_FORMAT must be",
		},
		{
			name: "enrollment requires a secret key",
			env: map[string]string{
				"FLEET_ENROLLMENT_ENABLED": "true",
			},
			wantErr: "enrollment requires a 32-byte encryption key",
		},
		{
			name: "enrollment requires TLS and Core client CA",
			env: map[string]string{
				"FLEET_ENROLLMENT_ENABLED":        "true",
				"FLEET_ENROLLMENT_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
				"FLEET_ENROLLMENT_CORE_CLIENT_CN": "fleet-core",
				"FLEET_ENROLLMENT_OPAMP_ENDPOINT": "wss://fleet.example.test/v1/opamp",
			},
			wantErr: "enrollment requires HTTPS and a dedicated Core client CA",
		},
		{
			name: "enrollment requires the secure OpAMP listener",
			env: map[string]string{
				"FLEET_ENROLLMENT_ENABLED":        "true",
				"FLEET_ENROLLMENT_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
				"FLEET_ENROLLMENT_CORE_CLIENT_CN": "fleet-core",
				"FLEET_ENROLLMENT_OPAMP_ENDPOINT": "wss://fleet.example.test/v1/opamp",
				"FLEET_HTTP_TLS_CERT_FILE":        "http.crt",
				"FLEET_HTTP_TLS_KEY_FILE":         "http.key",
				"FLEET_HTTP_CLIENT_CA_FILE":       "core-ca.crt",
				"OPAMP_TLS_CERT_FILE":             "opamp.crt",
				"OPAMP_TLS_KEY_FILE":              "opamp.key",
			},
			wantErr: "enrollment requires the OpAMP listener to be enabled with TLS",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(mapLookup(tc.env))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
