package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestLoggerFiltersLevels verifies filtering and structured component fields.
func TestLoggerFiltersLevels(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer

			logger, err := New(&output, Options{
				Level:   "info",
				Format:  format,
				Service: "fleet-manager",
			})
			if err != nil {
				t.Fatalf("create logger: %v", err)
			}

			component := logger.With("component", "mongodb")
			component.Debug("hidden diagnostic")
			component.Info("database connected")

			if bytes.Contains(output.Bytes(), []byte("hidden diagnostic")) {
				t.Fatal("debug record should be filtered")
			}

			if !bytes.Contains(output.Bytes(), []byte("database connected")) {
				t.Fatal("info record was not written")
			}

			if format == "json" {
				var record map[string]any
				if err := json.Unmarshal(output.Bytes(), &record); err != nil {
					t.Fatalf("decode JSON record: %v", err)
				}

				if record["level"] != "INFO" ||
					record["service"] != "fleet-manager" ||
					record["component"] != "mongodb" {
					t.Fatalf("unexpected record fields: %v", record)
				}
			}
		})
	}
}

// TestLoggerRejectsInvalidOptions prevents silent configuration fallback.
func TestLoggerRejectsInvalidOptions(t *testing.T) {
	cases := []Options{
		{Level: "trace", Format: "json", Service: "fleet"},
		{Level: "info", Format: "xml", Service: "fleet"},
		{Level: "info", Format: "json"},
	}

	for _, cfg := range cases {
		if _, err := New(&bytes.Buffer{}, cfg); err == nil {
			t.Fatalf("expected invalid options to fail: %+v", cfg)
		}
	}
}
