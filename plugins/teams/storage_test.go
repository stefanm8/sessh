package teams

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapturePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SESSH_HOME", home)
	for _, key := range []string{"TEAMS_DATA_DIR", "TEAMS_MESSAGES_FILE", "TEAMS_CONVERSATIONS_FILE"} {
		t.Setenv(key, "")
	}
	var cfg captureConfig
	if err := cfg.resolve(); err != nil {
		t.Fatal(err)
	}
	w, err := openCaptureWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.writeConversationsFile(); err != nil {
		t.Fatal(err)
	}
	path, err := ResolveMessagesPath("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "teams", "messages.jsonl"); path != want || cfg.messagesPath != want {
		t.Fatalf("capture/query paths = %q/%q, want %q", cfg.messagesPath, path, want)
	}
	for _, path := range []string{filepath.Dir(path), path, cfg.convPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s exposes captured data: %o", path, info.Mode().Perm())
		}
	}
	t.Setenv("TEAMS_DATA_DIR", filepath.Join(home, "custom"))
	if DataDirDefault() != filepath.Join(home, "custom") {
		t.Fatal("TEAMS_DATA_DIR must override SESSH_HOME")
	}
}

func TestCaptureWriterExcludesDuplicates(t *testing.T) {
	dir := t.TempDir()
	cfg := captureConfig{messagesPath: filepath.Join(dir, "messages.jsonl"), convPath: filepath.Join(dir, "conversations.jsonl")}
	first, err := openCaptureWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := openCaptureWriter(cfg)
	if err == nil {
		second.Close()
		t.Fatal("two captures can write the same store")
	}
	first.Close()
	second, err = openCaptureWriter(cfg)
	if err != nil {
		t.Fatal("lock not released:", err)
	}
	second.Close()
}
