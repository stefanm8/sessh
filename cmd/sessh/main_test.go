package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/plugin"
)

func isolatedConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SESSH_HOME", home)
	for _, key := range []string{"SESSH_PROFILE", "SESSH_KEEP_ALIVE", "BITBUCKET_WORKSPACE", "JIRA_SITE", "CONFLUENCE_SITE"} {
		t.Setenv(key, "")
	}
	return home
}

func TestKeepAliveFlags(t *testing.T) {
	isolatedConfig(t)
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--keep-alive", "7m", "--no-keep-alive", "jira", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	flags := plugin.GlobalFlags()
	if flags.KeepAlive != "7m" || !flags.NoKeepAlive {
		t.Fatalf("flags were not bound: %+v", flags)
	}
	if !strings.Contains(out.String(), "--keep-alive") || !strings.Contains(out.String(), "--no-keep-alive") {
		t.Fatal("warm browser controls missing from command help")
	}
}

func TestInvalidKeepAliveDoesNotOpenBrowser(t *testing.T) {
	for _, value := range []string{"-1m", "tomorrow"} {
		t.Run(value, func(t *testing.T) {
			home := isolatedConfig(t)
			t.Setenv("JIRA_SITE", "https://tenant.example")
			root := newRootCommand()
			root.SetArgs([]string{"jira", "project", "list", "--keep-alive=" + value})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "invalid keep-alive") {
				t.Fatalf("error = %v, want keep-alive validation", err)
			}
			if _, err := os.Stat(filepath.Join(home, "profiles")); !os.IsNotExist(err) {
				t.Fatalf("invalid option touched browser profiles: %v", err)
			}
		})
	}
}

func TestMissingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		setting string
	}{
		{[]string{"jira", "issue", "list"}, "JIRA_SITE"},
		{[]string{"jira", "issue", "view", "ENG-1", "--web"}, "JIRA_SITE"},
		{[]string{"confluence", "space", "list"}, "CONFLUENCE_SITE"},
		{[]string{"wiki", "page", "view", "123", "--web"}, "CONFLUENCE_SITE"},
		{[]string{"bb", "repo", "list"}, "BITBUCKET_WORKSPACE"},
		{[]string{"bitbucket", "repo", "list", "--web"}, "BITBUCKET_WORKSPACE"},
		{[]string{"auth", "login", "jira"}, "JIRA_SITE"},
		{[]string{"auth", "login", "confluence"}, "CONFLUENCE_SITE"},
		{[]string{"auth", "login", "bitbucket"}, "BITBUCKET_WORKSPACE"},
		{[]string{"auth", "status", "jira"}, "JIRA_SITE"},
		{[]string{"daemon", "jira"}, "JIRA_SITE"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			home := isolatedConfig(t)
			root := newRootCommand()
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.setting) {
				t.Fatalf("got %v, want configuration error naming %s", err, tc.setting)
			}
			if _, err := os.Stat(filepath.Join(home, "profiles")); !os.IsNotExist(err) {
				t.Fatalf("configuration failure touched browser profiles: %v", err)
			}
		})
	}
}

func TestHelpWithoutConfiguration(t *testing.T) {
	isolatedConfig(t)
	for _, args := range [][]string{{"--help"}, {"--version"}, {"jira", "--help"}, {"confluence", "--help"}, {"bitbucket", "--help"}, {"teams", "--help"}} {
		root := newRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "sessh") {
			t.Fatalf("%v missing executable name: %s", args, out.String())
		}
	}
}

func TestConfiguredPlugins(t *testing.T) {
	isolatedConfig(t)
	t.Setenv("JIRA_SITE", "https://tenant.example/")
	t.Setenv("CONFLUENCE_SITE", "https://tenant.example")
	t.Setenv("BITBUCKET_WORKSPACE", "example-workspace")
	for _, p := range plugin.All() {
		if err := plugin.Validate(p); err != nil {
			t.Errorf("%s: %v", p.Name(), err)
		}
	}
}
