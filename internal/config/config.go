// Package config loads and stores the ocm hosts registry.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Host describes one remote machine running (or able to run) opencode serve.
// Local opencode servers are not configured here: they are auto-discovered.
type Host struct {
	// SSH is the ssh destination (alias from ~/.ssh/config or user@host).
	SSH string `json:"ssh"`
	// RemotePort is the port opencode serve listens on, on the remote machine.
	RemotePort int `json:"remote_port"`
	// LocalPort is the local port the ssh tunnel binds to.
	LocalPort int `json:"local_port"`
	// Opencode is the path of the opencode binary on the remote machine.
	Opencode string `json:"opencode"`
	// Dir is the default remote working directory (optional).
	Dir string `json:"dir,omitempty"`
	// Password protects the remote server with HTTP basic auth (optional).
	// It is exported as OPENCODE_SERVER_PASSWORD when ocm starts the remote
	// server, and used by ocm and the attached TUI to authenticate.
	// The basic auth username is opencode's default ("opencode").
	Password string `json:"password,omitempty"`
}

// Config is the on-disk ocm configuration.
type Config struct {
	Hosts map[string]Host `json:"hosts"`
	// Password is the default password for every server (optional). A
	// host's own "password" field and "local_password" override it.
	// Load propagates it in memory; it is never copied back to disk.
	Password string `json:"password,omitempty"`
	// LocalPassword protects local servers started by ocm with HTTP basic
	// auth (optional), and is used when probing/attaching to local servers.
	LocalPassword string `json:"local_password,omitempty"`
}

var validOpencodeRe = regexp.MustCompile(`^[a-zA-Z0-9._/~ -]+$`)

// ValidOpencode reports whether s is safe to use as a remote opencode binary
// path in a shell command. The path may start with ~ and contain spaces (e.g.
// for wrapper scripts with arguments), but shell metacharacters are rejected.
func ValidOpencode(s string) bool {
	return validOpencodeRe.MatchString(s)
}

// Names returns host names sorted alphabetically.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Hosts))
	for n := range c.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the host by name; as a convenience, a name that is a unique
// substring of exactly one configured host also matches.
func (c *Config) Get(name string) (string, Host, error) {
	if h, ok := c.Hosts[name]; ok {
		return name, h, nil
	}
	var matches []string
	for n := range c.Hosts {
		if strings.Contains(n, name) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return matches[0], c.Hosts[matches[0]], nil
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", Host{}, fmt.Errorf("host %q is ambiguous: %s", name, strings.Join(matches, ", "))
	}
	return "", Host{}, fmt.Errorf("unknown host %q (known: %s)", name, strings.Join(c.Names(), ", "))
}

// Path returns the config file path.
func Path() string {
	if p := os.Getenv("OCM_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ocm", "config.json")
}

// Default is the configuration written on first run.
func Default() *Config {
	return &Config{
		Hosts: map[string]Host{
			// Example entry: replace with your own hosts. SSH is an alias
			// from ~/.ssh/config or a user@host destination.
			"example": {
				SSH:        "example",
				RemotePort: 4096,
				LocalPort:  14001,
				Opencode:   "~/.opencode/bin/opencode",
			},
		},
	}
}

// applyDefaultPassword fills empty per-host and local passwords with the
// top-level default. In-memory only: Save must not run after this, or the
// propagated copies would be written to disk.
func (c *Config) applyDefaultPassword() {
	if c.Password == "" {
		return
	}
	for name, h := range c.Hosts {
		if h.Password == "" {
			h.Password = c.Password
			c.Hosts[name] = h
		}
	}
	if c.LocalPassword == "" {
		c.LocalPassword = c.Password
	}
}

// Load reads the config file, creating it with defaults if missing.
func Load() (*Config, error) {
	p := Path()
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		cfg := Default()
		if err := Save(cfg); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
		fmt.Fprintf(os.Stderr, "ocm: created default config at %s\n", p)
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]Host{}
	}
	// Migration: local instances are now auto-discovered, drop legacy
	// fixed "local" entries. Only entries that are recognizably the old
	// local host are removed; any other host without an ssh destination is
	// treated as a config mistake below, never silently deleted.
	changed := false
	for name, h := range cfg.Hosts {
		if h.SSH == "local" || (name == "local" && h.SSH == "") {
			delete(cfg.Hosts, name)
			changed = true
		}
	}
	if changed {
		if err := Save(&cfg); err == nil {
			fmt.Fprintf(os.Stderr, "ocm: removed legacy local host entry from %s (local servers are auto-discovered now)\n", p)
		}
	}
	for name, h := range cfg.Hosts {
		if h.SSH == "" {
			return nil, fmt.Errorf("host %q: missing \"ssh\" destination", name)
		}
		// Reject opencode paths with shell metacharacters; the path is used
		// inside a remote shell command over SSH.
		if !ValidOpencode(h.Opencode) {
			return nil, fmt.Errorf("host %q: opencode path %q contains unsafe characters; only letters, digits, ., /, _, ~, -, and spaces are allowed", name, h.Opencode)
		}
	}
	cfg.applyDefaultPassword()
	return &cfg, nil
}

// Save writes the config to disk.
func Save(cfg *Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the config may contain server passwords.
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	// WriteFile only applies the mode on creation; tighten pre-existing files.
	return os.Chmod(p, 0o600)
}

// SafeMarshal returns a copy of raw config JSON data with password fields
// redacted. It is used by the "ocm config" command so that passwords are
// never printed to the terminal, even accidentally.
func SafeMarshal(raw []byte) string {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return string(raw)
	}
	maskPasswords(cfg)
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func maskPasswords(v any) {
	switch vv := v.(type) {
	case map[string]any:
		for k, val := range vv {
			if (k == "password" || k == "local_password") && val != nil {
				if s, ok := val.(string); ok && s != "" {
					vv[k] = "***"
				}
				continue
			}
			maskPasswords(val)
		}
	case []any:
		for _, item := range vv {
			maskPasswords(item)
		}
	}
}
