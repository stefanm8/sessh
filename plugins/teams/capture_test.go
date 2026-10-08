package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func TestCaptureDefaultsToBackground(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sessh-capture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("SESSH_HOME", home)
	oldFlags := plugin.GlobalFlags()
	plugin.BindFlags(&plugin.Flags{Profile: "test"})
	t.Cleanup(func() { plugin.BindFlags(oldFlags) })
	owner := &Teams{}
	spec := plugin.Service("teams capture", owner)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- spec.Run(ctx, func(ctx context.Context, report func(string, string)) error {
			report("running", "test capture")
			close(ready)
			<-ctx.Done()
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("test worker did not start")
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"start"}, ""},
		{[]string{"-d"}, ""},
		{[]string{"--foreground"}, "already running or cannot be locked"},
		{[]string{"--once"}, "already running or cannot be locked"},
		{[]string{"start", "--once"}, "background capture cannot use"},
		{[]string{"start", "--foreground"}, "background capture cannot use"},
		{[]string{"-d", "--foreground"}, "background capture cannot use"},
		{[]string{"-d", "--once"}, "background capture cannot use"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := &cobra.Command{Use: "sessh", SilenceUsage: true, SilenceErrors: true}
			root.AddCommand(owner.Command())
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(append([]string{"teams", "capture"}, tc.args...))
			err := root.ExecuteContext(context.Background())
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
			} else if err != nil || !strings.Contains(out.String(), "Already running.") || !strings.Contains(out.String(), "capture monitor --profile \"test\"") {
				t.Fatalf("did not reuse daemon: %s: %v", out.String(), err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(home, "profiles")); !os.IsNotExist(err) {
		t.Fatalf("repeated start touched browser profiles: %v", err)
	}
}

type progressSession struct {
	session.Session
	err        error
	incomplete bool
}

func (s *progressSession) Eval(_ context.Context, js string, _, out any) error {
	if s.err != nil {
		return s.err
	}
	if js == jsPoll {
		return json.Unmarshal([]byte(`{"capture":{"installed":true},"scroll":{"installed":true}}`), out)
	}
	if js == jsDumpIDB {
		if s.incomplete {
			return json.Unmarshal([]byte(`{"messages":[],"errors":["replychain-manager DB not found"]}`), out)
		}
		return json.Unmarshal([]byte(`{"messages":[{"id":"example-message","messageType":"Text","content":"A synthetic message"}],"conversations":[]}`), out)
	}
	return errors.New("unexpected evaluation")
}

func TestCaptureProgressTracksSuccessfulWork(t *testing.T) {
	dir := t.TempDir()
	var state, detail string
	cfg := captureConfig{
		messagesPath: filepath.Join(dir, "messages.jsonl"),
		convPath:     filepath.Join(dir, "conversations.jsonl"),
		report:       func(s, d string, _ json.RawMessage) { state, detail = s, d },
	}
	w, err := openCaptureWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s := &progressSession{}
	r := &captureRun{cfg: cfg, w: w, s: s}
	ctx := context.Background()
	if err := r.note(r.harvest(ctx)); err != nil {
		t.Fatal(err)
	}
	if state != "running" || w.written != 1 || w.lastHarvest.IsZero() || !w.lastPoll.IsZero() || !strings.Contains(detail, "1 total, 1 new this run") {
		t.Fatalf("unexpected harvest progress: %s: %s", state, detail)
	}
	if err := r.note(r.poll(ctx)); err != nil {
		t.Fatal(err)
	}
	pollAt, harvestAt := w.lastPoll, w.lastHarvest
	if pollAt.IsZero() || strings.Contains(detail, "not yet") {
		t.Fatalf("idle poll did not advance progress: %s", detail)
	}
	s.err = errors.New("browser offline")
	_ = r.note(r.poll(ctx))
	_ = r.note(r.harvest(ctx))
	if state != "recovering" || !w.lastPoll.Equal(pollAt) || !w.lastHarvest.Equal(harvestAt) || !strings.Contains(detail, "browser offline") || !strings.Contains(detail, "1 total, 1 new this run") {
		t.Fatalf("failure lost progress or advanced timestamps: %s: %s", state, detail)
	}
	// A browser retry shares the writer; neither duplicates nor a new session
	// reset the durable capture counts.
	s.err = nil
	r = &captureRun{cfg: cfg, w: w, s: s}
	if err := r.note(r.harvest(ctx)); err != nil {
		t.Fatal(err)
	}
	if state != "running" || w.written != 1 || len(w.seenIDs) != 1 || !w.lastPoll.Equal(pollAt) {
		t.Fatalf("retry reset progress or duplicated messages: %s: %s", state, detail)
	}
	w.Close()
	w, err = openCaptureWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cfg.reportProgress(w, "starting", "restarted")
	if !strings.Contains(detail, "1 total, 0 new this run") {
		t.Fatalf("existing messages counted as new: %s", detail)
	}
}

func TestIncompleteHarvestIsNotReady(t *testing.T) {
	dir := t.TempDir()
	var progress captureProgress
	cfg := captureConfig{
		messagesPath: filepath.Join(dir, "messages.jsonl"),
		convPath:     filepath.Join(dir, "conversations.jsonl"),
		report: func(_, _ string, data json.RawMessage) {
			if err := json.Unmarshal(data, &progress); err != nil {
				t.Fatal(err)
			}
		},
	}
	w, err := openCaptureWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r := &captureRun{cfg: cfg, w: w, s: &progressSession{incomplete: true}}
	ctx := context.Background()
	err = r.harvest(ctx)
	if err == nil || !strings.Contains(err.Error(), "replychain-manager DB not found") {
		t.Fatalf("incomplete harvest error = %v", err)
	}
	_ = r.note(err)
	_ = r.note(r.poll(ctx))
	if progress.Ready || !progress.LastHarvest.IsZero() || progress.LastPoll.IsZero() {
		t.Fatalf("a successful poll made an incomplete harvest ready: %+v", progress)
	}
}
