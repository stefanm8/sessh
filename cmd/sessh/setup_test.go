package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShellPathFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	check := func(want ...string) {
		t.Helper()
		got, err := shellPathFiles("")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v %v, want %v", got, err, want)
		}
	}
	check(filepath.Join(home, ".bashrc"), filepath.Join(home, ".profile"))
	os.WriteFile(filepath.Join(home, ".bash_login"), nil, 0o600)
	check(filepath.Join(home, ".bashrc"), filepath.Join(home, ".bash_login"))
	os.WriteFile(filepath.Join(home, ".bash_profile"), nil, 0o600)
	check(filepath.Join(home, ".bashrc"), filepath.Join(home, ".bash_profile"))
	t.Setenv("SHELL", "/bin/zsh")
	zdot := filepath.Join(home, "zsh-config")
	t.Setenv("ZDOTDIR", zdot)
	check(filepath.Join(zdot, ".zshrc"), filepath.Join(zdot, ".zprofile"))
	t.Setenv("SHELL", "/bin/fish")
	explicit := filepath.Join(home, "custom-rc")
	got, err := shellPathFiles(explicit)
	if err != nil || !reflect.DeepEqual(got, []string{explicit}) {
		t.Fatalf("explicit rc: %v %v", got, err)
	}
}

func TestPathSetupFindsInstalledBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin ' $dollars [brackets] and spaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "sessh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'installed sessh'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(t.TempDir(), "rc")
	original := "export USER_SETTING=preserved\n"
	os.WriteFile(rc, []byte(original), 0o640)
	for range 2 {
		if err := updateShellBlock(rc, pathBegin, pathEnd, pathBlock(dir)); err != nil {
			t.Fatal(err)
		}
	}
	// A separate autostart entry must survive PATH setup and vice versa.
	auto := autostartBegin + "\n# existing autostart\n" + autostartEnd + "\n"
	if err := updateAutostart(rc, auto); err != nil {
		t.Fatal(err)
	}
	if err := updateShellBlock(rc, pathBegin, pathEnd, pathBlock(dir)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(rc)
	if !strings.HasPrefix(string(data), original) || strings.Count(string(data), pathBegin) != 1 || !strings.Contains(string(data), auto) {
		t.Fatalf("rc: %s", data)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `. "$1"; . "$1"; command -v sessh; sessh; printf '\n%s\n%s\n' "$PATH" "$USER_SETTING"`, "bash", rc)
	cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), binary+"\ninstalled sessh\n") || strings.Count(strings.Split(string(out), "\n")[2], dir) != 1 || !strings.HasSuffix(string(out), "preserved\n") {
		t.Fatalf("fresh shell: %s %v", out, err)
	}
	if info, _ := os.Stat(rc); info.Mode().Perm() != 0o640 {
		t.Fatal("changed permissions")
	}
}

func TestSetupDoesNotNeedConfiguration(t *testing.T) {
	isolatedConfig(t)
	rc := filepath.Join(t.TempDir(), "shellrc")
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"setup", "--rc", rc})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rc)
	if err != nil || !strings.Contains(string(data), pathBegin) {
		t.Fatalf("missing PATH entry: %s %v", data, err)
	}
}
