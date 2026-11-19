package config

import (
	"strings"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/env"
)

func newTestEnvReader(t *testing.T, values map[string]string) env.Reader {
	t.Helper()

	reader, err := env.NewReader(func(key string) (string, bool) {
		val, ok := values[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("failed to create env reader: %v", err)
	}
	return reader
}

func validHTTPConfig() HTTPConfig {
	return HTTPConfig{
		ListenAddress:     "127.0.0.1:8081",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadinessTimeout:  2 * time.Second,
	}
}

func TestLoadHTTPConfig_Defaults(t *testing.T) {
	reader := newTestEnvReader(t, nil)

	cfg, err := loadHTTPConfig(reader)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := validHTTPConfig()
	if cfg != want {
		t.Fatalf("got config %+v, want %+v", cfg, want)
	}
}

func TestLoadHTTPConfig_CustomValuesAndPortZero(t *testing.T) {
	reader := newTestEnvReader(t, map[string]string{
		"FLEET_HTTP_LISTEN_ADDRESS":      "127.0.0.1:0",
		"FLEET_HTTP_READ_HEADER_TIMEOUT": "2s",
		"FLEET_HTTP_READ_TIMEOUT":        "8s",
		"FLEET_HTTP_WRITE_TIMEOUT":       "12s",
		"FLEET_HTTP_IDLE_TIMEOUT":        "30s",
		"FLEET_HTTP_READINESS_TIMEOUT":   "1s",
	})

	cfg, err := loadHTTPConfig(reader)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := HTTPConfig{
		ListenAddress:     "127.0.0.1:0",
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       8 * time.Second,
		WriteTimeout:      12 * time.Second,
		IdleTimeout:       30 * time.Second,
		ReadinessTimeout:  1 * time.Second,
	}
	if cfg != want {
		t.Fatalf("got config %+v, want %+v", cfg, want)
	}
}

func TestLoadHTTPConfig_ReaderErrors(t *testing.T) {
	tests := []struct {
		name       string
		envVars    map[string]string
		wantErrMsg string
	}{
		{
			name: "empty listen address",
			envVars: map[string]string{
				"FLEET_HTTP_LISTEN_ADDRESS": "   ",
			},
			wantErrMsg: "FLEET_HTTP_LISTEN_ADDRESS must not be empty",
		},
		{
			name: "invalid read header timeout duration",
			envVars: map[string]string{
				"FLEET_HTTP_READ_HEADER_TIMEOUT": "not-a-duration",
			},
			wantErrMsg: "FLEET_HTTP_READ_HEADER_TIMEOUT must be a valid duration",
		},
		{
			name: "empty readiness timeout",
			envVars: map[string]string{
				"FLEET_HTTP_READINESS_TIMEOUT": "",
			},
			wantErrMsg: "FLEET_HTTP_READINESS_TIMEOUT must not be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newTestEnvReader(t, tc.envVars)

			_, err := loadHTTPConfig(reader)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
			}
			if !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrMsg, err.Error())
			}
		})
	}
}

func TestHTTPConfig_Validate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(c *HTTPConfig)
		wantErrMsg string
	}{
		{
			name:   "valid default config",
			mutate: func(c *HTTPConfig) {},
		},
		{
			name: "valid ipv6 loopback with ephemeral port",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "[::1]:0"
			},
		},
		{
			name: "missing port",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "127.0.0.1"
			},
			wantErrMsg: "invalid http listen address",
		},
		{
			name: "non-loopback ip 0.0.0.0",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "0.0.0.0:8081"
			},
			wantErrMsg: "explicit loopback ip",
		},
		{
			name: "non-loopback private ip",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "192.168.1.10:8081"
			},
			wantErrMsg: "explicit loopback ip",
		},
		{
			name: "hostname instead of ip",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "localhost:8081"
			},
			wantErrMsg: "explicit loopback ip",
		},
		{
			name: "port out of range",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "127.0.0.1:70000"
			},
			wantErrMsg: "port must be between 0 and 65535",
		},
		{
			name: "negative port",
			mutate: func(c *HTTPConfig) {
				c.ListenAddress = "127.0.0.1:-1"
			},
			wantErrMsg: "port must be between 0 and 65535",
		},
		{
			name: "non-positive timeout",
			mutate: func(c *HTTPConfig) {
				c.IdleTimeout = 0
			},
			wantErrMsg: "http timeouts must be positive",
		},
		{
			name: "readiness timeout equals read timeout",
			mutate: func(c *HTTPConfig) {
				c.ReadinessTimeout = c.ReadTimeout
			},
			wantErrMsg: "readiness timeout must be shorter",
		},
		{
			name: "readiness timeout greater than write timeout",
			mutate: func(c *HTTPConfig) {
				c.WriteTimeout = 3 * time.Second
				c.ReadinessTimeout = 4 * time.Second
			},
			wantErrMsg: "readiness timeout must be shorter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validHTTPConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if tc.wantErrMsg == "" {
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
			}
			if !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrMsg, err.Error())
			}
		})
	}
}
