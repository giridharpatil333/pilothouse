// Package config loads ~/.pilothouse/config.yaml. Every field has a working
// default so a missing file is valid.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xasdta/pilothouse/internal/paths"
)

type Config struct {
	// Web is the HTTP listener for the web app and the device API.
	// It binds to loopback only; remote access goes through Tailscale Serve.
	Web struct {
		Addr      string `yaml:"addr"`       // default 127.0.0.1:7517
		PublicURL string `yaml:"public_url"` // e.g. https://mac.tailnet.ts.net, set by `pilothouse remote tailscale`
	} `yaml:"web"`

	Presence struct {
		// IdleThreshold: OS idle longer than this means "away".
		IdleThreshold Duration `yaml:"idle_threshold"` // default 60s
		// NotifyAfter: when at the desk and a native dialog is waiting this
		// long, send a desktop notification anyway.
		NotifyAfter Duration `yaml:"notify_after"` // default 45s
	} `yaml:"presence"`

	Approvals struct {
		// HoldLimit is how long a request is held for remote devices before
		// it is handed back to the agent's own dialog. Clamped per agent
		// below the vendor's hook timeout.
		HoldLimit Duration `yaml:"hold_limit"` // default 9m
		// AlwaysHold routes every permission request to devices, even at the desk.
		AlwaysHold bool `yaml:"always_hold"`
		// FailClosed makes the hook shim deny pre_tool calls when the daemon is down.
		FailClosed bool `yaml:"fail_closed"`
	} `yaml:"approvals"`

	Notify struct {
		Desktop bool `yaml:"desktop"` // default true
		Ntfy    struct {
			Server string `yaml:"server"` // default https://ntfy.sh
			Topic  string `yaml:"topic"`  // empty = off
			Token  string `yaml:"token"`
		} `yaml:"ntfy"`
		Webhooks []string `yaml:"webhooks"` // Slack/Discord-compatible incoming webhook URLs
	} `yaml:"notify"`

	// Retention is how long closed requests and raw hook events are kept.
	Retention Duration `yaml:"retention"` // default 30 days

	// UpdateCheck sends {version, os, arch, monthly-rotating id} once a day.
	UpdateCheck *bool `yaml:"update_check"` // default true
}

// Duration is a time.Duration that reads "45s" / "9m" in YAML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.Duration.String(), nil }

// Default returns the configuration used when no file exists.
func Default() *Config {
	c := &Config{}
	c.Web.Addr = "127.0.0.1:7517"
	c.Presence.IdleThreshold = Duration{60 * time.Second}
	c.Presence.NotifyAfter = Duration{45 * time.Second}
	c.Approvals.HoldLimit = Duration{9 * time.Minute}
	c.Notify.Desktop = true
	c.Notify.Ntfy.Server = "https://ntfy.sh"
	c.Retention = Duration{30 * 24 * time.Hour}
	return c
}

// Load reads paths.Config(), applying defaults for missing fields.
func Load() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(paths.Config())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", paths.Config(), err)
	}
	return c, c.Validate()
}

func (c *Config) Validate() error {
	if c.Presence.IdleThreshold.Duration < 5*time.Second {
		return errors.New("presence.idle_threshold must be at least 5s")
	}
	if c.Approvals.HoldLimit.Duration < 10*time.Second {
		return errors.New("approvals.hold_limit must be at least 10s")
	}
	return nil
}

// UpdateCheckEnabled reports whether the anonymous update check is on.
func (c *Config) UpdateCheckEnabled() bool {
	if os.Getenv("PILOTHOUSE_NO_UPDATE_CHECK") != "" {
		return false
	}
	return c.UpdateCheck == nil || *c.UpdateCheck
}

// Save writes the config atomically with 0600 permissions.
func (c *Config) Save() error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	tmp := paths.Config() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, paths.Config())
}
