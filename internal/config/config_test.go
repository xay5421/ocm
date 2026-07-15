package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFrom(t *testing.T, content string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCM_CONFIG", p)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDefaultPasswordPropagation(t *testing.T) {
	cfg := loadFrom(t, `{
		"password": "global",
		"hosts": {
			"a": {"ssh": "a", "remote_port": 1, "local_port": 2},
			"b": {"ssh": "b", "remote_port": 1, "local_port": 3, "password": "own"}
		}
	}`)
	if got := cfg.Hosts["a"].Password; got != "global" {
		t.Errorf("host a: password = %q, want inherited %q", got, "global")
	}
	if got := cfg.Hosts["b"].Password; got != "own" {
		t.Errorf("host b: password = %q, want per-host override %q", got, "own")
	}
	if got := cfg.LocalPassword; got != "global" {
		t.Errorf("local_password = %q, want inherited %q", got, "global")
	}
}

func TestLocalPasswordOverridesDefault(t *testing.T) {
	cfg := loadFrom(t, `{"password": "global", "local_password": "loc", "hosts": {}}`)
	if got := cfg.LocalPassword; got != "loc" {
		t.Errorf("local_password = %q, want %q", got, "loc")
	}
}

func TestNoDefaultPassword(t *testing.T) {
	cfg := loadFrom(t, `{"hosts": {"a": {"ssh": "a", "remote_port": 1, "local_port": 2}}}`)
	if got := cfg.Hosts["a"].Password; got != "" {
		t.Errorf("host a: password = %q, want empty", got)
	}
	if cfg.LocalPassword != "" {
		t.Errorf("local_password = %q, want empty", cfg.LocalPassword)
	}
}
