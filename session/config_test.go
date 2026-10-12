package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Load caches the config for the life of the process, which is exactly right
// for a CLI and exactly wrong for a table of tests. resetConfig drops the cache
// so each subtest starts from a clean read.
func resetConfig() {
	cfgOnce = sync.Once{}
	cfg = Config{}
}

// isolatedHome points ~/.sessh at a temp dir, so nothing here can read or write
// the developer's real profiles, and clears the env that outranks the file.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SESSH_HOME", home)
	t.Setenv("SESSH_PROFILE", "")
	resetConfig()
	t.Cleanup(resetConfig)
	return home
}

// writeConfig drops a config.yaml into the isolated home and invalidates the
// cache so the next Load sees it.
func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	resetConfig()
}

func TestRoot(t *testing.T) {
	t.Run("SESSH_HOME wins when set", func(t *testing.T) {
		home := isolatedHome(t)
		if got := Root(); got != home {
			t.Errorf("Root() = %q, want %q", got, home)
		}
	})
	t.Run("falls back to ~/.sessh", func(t *testing.T) {
		t.Setenv("SESSH_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory in this environment")
		}
		if want := filepath.Join(home, ".sessh"); Root() != want {
			t.Errorf("Root() = %q, want %q", Root(), want)
		}
	})
	t.Run("derived paths all hang off Root", func(t *testing.T) {
		home := isolatedHome(t)
		cases := map[string]string{
			"ProfileDir": ProfileDir("work"),
			"RunDir":     RunDir(),
			"SocketPath": SocketPath("work"),
			"ConfigPath": ConfigPath(),
		}
		for name, got := range cases {
			if rel, err := filepath.Rel(home, got); err != nil || rel == ".." || filepath.IsAbs(rel) {
				t.Errorf("%s() = %q, which is outside SESSH_HOME %q", name, got, home)
			}
		}
		if want := filepath.Join(home, "profiles", "work"); ProfileDir("work") != want {
			t.Errorf("ProfileDir = %q, want %q", ProfileDir("work"), want)
		}
		if want := filepath.Join(home, "run", "work.sock"); SocketPath("work") != want {
			t.Errorf("SocketPath = %q, want %q", SocketPath("work"), want)
		}
		if want := filepath.Join(home, "config.yaml"); ConfigPath() != want {
			t.Errorf("ConfigPath = %q, want %q", ConfigPath(), want)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("a missing file is an empty config, not an error", func(t *testing.T) {
		isolatedHome(t)
		c := Load()
		if c.Profile != "" || len(c.Plugins) != 0 {
			t.Errorf("Load() = %+v, want zero value", c)
		}
	})
	t.Run("unparseable YAML degrades to an empty config", func(t *testing.T) {
		isolatedHome(t)
		writeConfig(t, "profile: [unclosed\n\t\tnot yaml at all: :::")
		c := Load()
		if c.Profile != "" {
			t.Errorf("Load().Profile = %q, want empty after a bad parse", c.Profile)
		}
	})
	t.Run("a full config round-trips", func(t *testing.T) {
		isolatedHome(t)
		writeConfig(t, "profile: global\nplugins:\n  jira:\n    profile: work\n    site: https://tenant.example\n  bitbucket:\n    workspace: example-workspace\n")
		c := Load()
		if c.Profile != "global" {
			t.Errorf("Profile = %q, want %q", c.Profile, "global")
		}
		if got := c.Plugins["jira"]["site"]; got != "https://tenant.example" {
			t.Errorf("plugins.jira.site = %q", got)
		}
		if got := c.Plugins["bitbucket"]["workspace"]; got != "example-workspace" {
			t.Errorf("plugins.bitbucket.workspace = %q", got)
		}
	})
}

func TestSetting(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		plugin   string
		key      string
		envValue string
		fallback string
		want     string
	}{
		{
			name:   "the env value wins over everything",
			config: "plugins:\n  jira:\n    site: https://config.example\n",
			plugin: "jira", key: "site",
			envValue: "https://env.example",
			fallback: "https://default.example",
			want:     "https://env.example",
		},
		{
			name:   "config wins over the compiled default",
			config: "plugins:\n  jira:\n    site: https://config.example\n",
			plugin: "jira", key: "site",
			fallback: "https://default.example",
			want:     "https://config.example",
		},
		{
			name:   "the compiled default is the floor",
			config: "",
			plugin: "jira", key: "site",
			fallback: "https://default.example",
			want:     "https://default.example",
		},
		{
			name:   "an unrelated plugin's settings are not borrowed",
			config: "plugins:\n  bitbucket:\n    site: https://bitbucket.example\n",
			plugin: "jira", key: "site",
			fallback: "https://default.example",
			want:     "https://default.example",
		},
		{
			name:   "an unknown key falls through to the default",
			config: "plugins:\n  jira:\n    site: https://config.example\n",
			plugin: "jira", key: "workspace",
			fallback: "example-workspace",
			want:     "example-workspace",
		},
		{
			name:   "an empty config value is not a value",
			config: "plugins:\n  jira:\n    site: \"\"\n",
			plugin: "jira", key: "site",
			fallback: "https://default.example",
			want:     "https://default.example",
		},
		{
			name:   "everything empty yields the empty fallback",
			config: "",
			plugin: "jira", key: "site",
			fallback: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatedHome(t)
			if tt.config != "" {
				writeConfig(t, tt.config)
			}
			got := Setting(tt.plugin, tt.key, tt.envValue, tt.fallback)
			if got != tt.want {
				t.Errorf("Setting(%q, %q, %q, %q) = %q, want %q",
					tt.plugin, tt.key, tt.envValue, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestResolveProfileName(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		env      string
		plugin   string
		flag     string
		compiled string
		want     string
	}{
		{
			name:   "the flag beats the environment, the config and the default",
			config: "profile: fromglobal\nplugins:\n  jira:\n    profile: fromplugin\n",
			env:    "fromenv",
			plugin: "jira", flag: "fromflag", compiled: "atlassian",
			want: "fromflag",
		},
		{
			name:   "SESSH_PROFILE beats the config and the default",
			config: "profile: fromglobal\nplugins:\n  jira:\n    profile: fromplugin\n",
			env:    "fromenv",
			plugin: "jira", compiled: "atlassian",
			want: "fromenv",
		},
		{
			name:   "the per-plugin config entry beats the global one",
			config: "profile: fromglobal\nplugins:\n  jira:\n    profile: fromplugin\n",
			plugin: "jira", compiled: "atlassian",
			want: "fromplugin",
		},
		{
			name:   "the global config entry applies to a plugin with no entry",
			config: "profile: fromglobal\nplugins:\n  jira:\n    profile: fromplugin\n",
			plugin: "teams", compiled: "teams",
			want: "fromglobal",
		},
		{
			name:   "an empty per-plugin entry falls through to global",
			config: "profile: fromglobal\nplugins:\n  jira:\n    profile: \"\"\n",
			plugin: "jira", compiled: "atlassian",
			want: "fromglobal",
		},
		{
			name:   "the plugin's compiled name is used when nothing configures one",
			plugin: "jira", compiled: "atlassian",
			want: "atlassian",
		},
		{
			name:   "plugins sharing a compiled name share a profile",
			plugin: "confluence", compiled: "atlassian",
			want: "atlassian",
		},
		{
			name:   "with no compiled name at all we land on the default profile",
			plugin: "jira",
			want:   DefaultProfile,
		},
		{
			name:   "a config with only per-plugin entries still reaches the default",
			config: "plugins:\n  jira:\n    site: https://x\n",
			plugin: "jira",
			want:   DefaultProfile,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatedHome(t)
			if tt.config != "" {
				writeConfig(t, tt.config)
			}
			if tt.env != "" {
				t.Setenv("SESSH_PROFILE", tt.env)
			}
			got := ResolveProfileName(tt.plugin, tt.flag, tt.compiled)
			if got != tt.want {
				t.Errorf("ResolveProfileName(%q, %q, %q) = %q, want %q",
					tt.plugin, tt.flag, tt.compiled, got, tt.want)
			}
		})
	}
}

func TestValidateSiteSetting(t *testing.T) {
	for _, value := range []string{"", "tenant.example", "file:///tmp/site", "https://user:pass@tenant.example", "https://tenant.example/wiki", "https://tenant.example?token=secret", "https://tenant.example#fragment"} {
		if err := ValidateSiteSetting("jira", value, "JIRA_SITE"); err == nil {
			t.Errorf("accepted invalid site %q", value)
		}
	}
	for _, value := range []string{"https://tenant.example", "https://tenant.example/", "http://localhost:8080"} {
		if err := ValidateSiteSetting("jira", value, "JIRA_SITE"); err != nil {
			t.Errorf("rejected valid site %q: %v", value, err)
		}
	}
}
