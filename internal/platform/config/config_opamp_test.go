package config

import (
	"encoding/json"
	"testing"
)

func TestOpAMPGroupingPreservesJSON(t *testing.T) {
	cfg, err := LoadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	group := cfg.OpAMPConfig
	if group.OpAMPPath != "/v1/opamp" {
		t.Fatal("default changed")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["listen_address"] != "127.0.0.1:4320" || fields["opamp_path"] != "/v1/opamp" || fields["auth_timeout"] != float64(5000000000) {
		t.Fatalf("wire shape changed: %s", data)
	}
	if _, exists := fields["OpAMPConfig"]; exists {
		t.Fatal("nested config introduced")
	}
}
