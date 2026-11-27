package config

import (
	"strings"
	"testing"
)

// TestHTTPConfigTLSContract covers certificate pairing and listener safety.
func TestHTTPConfigTLSContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"cert_only", map[string]string{"FLEET_HTTP_TLS_CERT_FILE": "server.crt"}, "certificate and key must be configured together"},
		{"key_only", map[string]string{"FLEET_HTTP_TLS_KEY_FILE": "server.key"}, "certificate and key must be configured together"},
		{"client_CA_without_TLS", map[string]string{"FLEET_HTTP_CLIENT_CA_FILE": "client-ca.crt"}, "client CA requires TLS"},
		{"TLS_without_client_CA", map[string]string{"FLEET_HTTP_LISTEN_ADDRESS": "0.0.0.0:0", "FLEET_HTTP_TLS_CERT_FILE": "server.crt", "FLEET_HTTP_TLS_KEY_FILE": "server.key"}, ""},
		{"TLS_with_client_CA", map[string]string{"FLEET_HTTP_LISTEN_ADDRESS": "0.0.0.0:0", "FLEET_HTTP_TLS_CERT_FILE": "server.crt", "FLEET_HTTP_TLS_KEY_FILE": "server.key", "FLEET_HTTP_CLIENT_CA_FILE": "client-ca.crt"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadHTTPConfig(newTestEnvReader(t, tc.values))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TLSCertFile != tc.values["FLEET_HTTP_TLS_CERT_FILE"] || cfg.TLSKeyFile != tc.values["FLEET_HTTP_TLS_KEY_FILE"] || cfg.ClientCAFile != tc.values["FLEET_HTTP_CLIENT_CA_FILE"] {
				t.Fatal("TLS settings not preserved")
			}
		})
	}
}
