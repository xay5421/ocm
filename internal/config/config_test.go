package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFrom(t *testing.T, content string) *Config {
	t.Helper()
	cfg, err := tryLoadFrom(t, content)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func tryLoadFrom(t *testing.T, content string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCM_CONFIG", p)
	return Load()
}

func TestDefaultPasswordPropagation(t *testing.T) {
	cfg := loadFrom(t, `{
		"password": "global",
		"hosts": {
			"a": {"ssh": "a", "remote_port": 1, "local_port": 2, "opencode": "~/.opencode/bin/opencode"},
			"b": {"ssh": "b", "remote_port": 1, "local_port": 3, "opencode": "~/.opencode/bin/opencode", "password": "own"}
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

func TestMissingSSHRejectedNotDeleted(t *testing.T) {
	content := `{"hosts": {"typo": {"remote_port": 1, "local_port": 2, "opencode": "~/.opencode/bin/opencode"}}}`
	if _, err := tryLoadFrom(t, content); err == nil {
		t.Fatal("Load() = nil error, want error for host without ssh destination")
	}
	// The config file must be untouched: a config mistake is reported, not
	// silently deleted.
	data, err := os.ReadFile(os.Getenv("OCM_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("config file was rewritten:\n%s", data)
	}
}

func TestLegacyLocalEntryMigration(t *testing.T) {
	cfg := loadFrom(t, `{"hosts": {
		"local": {"ssh": "", "remote_port": 1, "local_port": 2, "opencode": "x"},
		"old":   {"ssh": "local", "remote_port": 1, "local_port": 3, "opencode": "x"},
		"keep":  {"ssh": "keep", "remote_port": 1, "local_port": 4, "opencode": "x"}
	}}`)
	if _, ok := cfg.Hosts["local"]; ok {
		t.Error("legacy empty-ssh 'local' entry was not removed")
	}
	if _, ok := cfg.Hosts["old"]; ok {
		t.Error(`legacy ssh=="local" entry was not removed`)
	}
	if _, ok := cfg.Hosts["keep"]; !ok {
		t.Error("regular host was removed by migration")
	}
}

func TestNoDefaultPassword(t *testing.T) {
	cfg := loadFrom(t, `{"hosts": {"a": {"ssh": "a", "remote_port": 1, "local_port": 2, "opencode": "~/.opencode/bin/opencode"}}}`)
	if got := cfg.Hosts["a"].Password; got != "" {
		t.Errorf("host a: password = %q, want empty", got)
	}
	if cfg.LocalPassword != "" {
		t.Errorf("local_password = %q, want empty", cfg.LocalPassword)
	}
}
