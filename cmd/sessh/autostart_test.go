package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartRoundTrip(t *testing.T) {
	home := isolatedConfig(t)
	rc := filepath.Join(home, ".bashrc")
	original := "export EDITOR=vim\n# leave this alone\n"
	if err := os.WriteFile(rc, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		root := newRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append(args, "--rc", rc))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	for range 2 {
		run("autostart", "enable", "teams", "--profile", "work", "--no-scroll")
	}
	data, _ := os.ReadFile(rc)
	if strings.Count(string(data), autostartBegin) != 1 || !strings.HasPrefix(string(data), original) || !strings.Contains(string(data), "'--profile=work' '--no-scroll'") {
		t.Fatalf("bad rc:\n%s", data)
	}
	if info, _ := os.Stat(rc); info.Mode().Perm() != 0o640 {
		t.Fatal("rc permissions changed")
	}
	if err := exec.Command("bash", "-n", rc).Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run("autostart", "status"), "'capture' 'start'") {
		t.Fatal("status missing capture command")
	}
	run("autostart", "disable")
	data, _ = os.ReadFile(rc)
	if string(data) != original {
		t.Fatalf("disable changed original content: %q", data)
	}
	run("autostart", "disable")
}

func TestAutostartSafeEdits(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfile")
	link := filepath.Join(dir, ".zshrc")
	original := "# user settings\n"
	os.WriteFile(target, []byte(original), 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	block := autostartBegin + "\ntrue\n" + autostartEnd + "\n"
	if err := updateAutostart(link, block); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced dotfile symlink")
	}
	if err := updateAutostart(link, ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{autostartBegin, autostartEnd, block + block} {
		os.WriteFile(target, []byte(bad), 0o600)
		if err := updateAutostart(link, block); err == nil {
			t.Fatal("malformed markers accepted")
		}
		got, _ := os.ReadFile(target)
		if string(got) != bad {
			t.Fatal("malformed rc was overwritten")
		}
	}
}

func TestShellQuote(t *testing.T) {
	value := "a path/'with quotes' $(echo unsafe) `echo unsafe` \"double\"\nnext"
	out, err := exec.Command("bash", "-c", "printf '%s' "+shellQuote(value)).Output()
	if err != nil || string(out) != value {
		t.Fatalf("quote roundtrip: %q %v", out, err)
	}
}
