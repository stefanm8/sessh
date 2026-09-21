package session

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// ValidateSiteSetting checks an explicitly configured service origin before
// any browser or profile is opened.
func ValidateSiteSetting(plugin, value, env string) error {
	if value == "" {
		return fmt.Errorf("%s site is not configured; set %s or plugins.%s.site in %s", plugin, env, plugin, ConfigPath())
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("%s site must be an http(s) origin such as https://acme.atlassian.net; check %s or plugins.%s.site", plugin, env, plugin)
	}
	return nil
}

// Config is ~/.sessh/config.yaml. Environment variables still
// work and win over the file; a flag wins over both.
//
//	profile: default            # profile used when a plugin names none
//	plugins:
//	  jira:
//	    profile: work           # this plugin uses a different browser identity
//	    site: https://acme.atlassian.net
//	  bitbucket:
//	    workspace: acme
type Config struct {
	Profile string                       `yaml:"profile"`
	Plugins map[string]map[string]string `yaml:"plugins"`
}

var (
	cfgOnce sync.Once
	cfg     Config
)

// ConfigPath is ~/.sessh/config.yaml.
func ConfigPath() string { return filepath.Join(Root(), "config.yaml") }

// Load reads the config once per process. A missing or unparseable file is
// simply an empty config — configuration is a convenience, never a gate.
func Load() Config {
	cfgOnce.Do(func() {
		b, err := os.ReadFile(ConfigPath())
		if err != nil {
			return
		}
		_ = yaml.Unmarshal(b, &cfg)
	})
	return cfg
}

// Setting resolves one plugin setting: env var first (callers pass the value
// they already read), then config, then the compiled-in default.
func Setting(plugin, key, envValue, fallback string) string {
	if envValue != "" {
		return envValue
	}
	c := Load()
	if p, ok := c.Plugins[plugin]; ok {
		if v, ok := p[key]; ok && v != "" {
			return v
		}
	}
	return fallback
}

// ResolveProfileName applies configuration precedence:
// --profile flag, then SESSH_PROFILE, then config (per-plugin, then global),
// then the plugin's compile-time name.
func ResolveProfileName(plugin, flag, compiled string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("SESSH_PROFILE"); v != "" {
		return v
	}
	c := Load()
	if p, ok := c.Plugins[plugin]; ok {
		if v, ok := p["profile"]; ok && v != "" {
			return v
		}
	}
	if c.Profile != "" {
		return c.Profile
	}
	if compiled != "" {
		return compiled
	}
	return DefaultProfile
}
