package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsInvalidConfigBeforeRuntimeStartup(t *testing.T) {
	const sentinel = "do-not-print-this-mongodb-secret"
	lookup := func(key string) (string, bool) {
		switch key {
		case "FLEET_LOG_LEVEL":
			return "trace", true
		case "FLEET_MONGODB_URI":
			return "mongodb://user:" + sentinel + "@localhost:27017", true
		default:
			return "", false
		}
	}

	var output bytes.Buffer
	logger, err := run(context.Background(), lookup, &output)
	if err == nil {
		t.Fatal("run accepted invalid configuration")
	}
	if logger != nil {
		t.Fatal("logger was created before config validation succeeded")
	}
	if !strings.Contains(err.Error(), "FLEET_LOG_LEVEL") {
		t.Fatalf("error does not identify the invalid setting: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(output.String(), sentinel) {
		t.Fatal("bootstrap exposed a secret environment value")
	}
	if output.Len() != 0 {
		t.Fatalf("runtime output began before configuration validation: %q", output.String())
	}
}
