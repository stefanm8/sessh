package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/plugin"
)

type captureSnapshot struct {
	status background.Status
	live   bool
}

type fakeCaptureService struct {
	states       []captureSnapshot
	reads        int
	starts       int
	args         []string
	startErr     error
	afterInspect func()
}

func (s *fakeCaptureService) Inspect(context.Context) (background.Status, bool, error) {
	i := min(s.reads, len(s.states)-1)
	s.reads++
	if s.afterInspect != nil {
		s.afterInspect()
	}
	return s.states[i].status, s.states[i].live, nil
}

func (s *fakeCaptureService) Start(_ context.Context, args []string) (background.Status, bool, error) {
	s.starts++
	s.args = args
	return background.Status{}, false, s.startErr
}

func captureState(state, path string, ready bool) captureSnapshot {
	p := captureProgress{MessagesFile: path, Ready: ready}
	if ready {
		p.LastHarvest = time.Now()
	}
	b, _ := json.Marshal(p)
	return captureSnapshot{status: background.Status{State: state, Progress: b}, live: true}
}

func TestWaitForCaptureReadiness(t *testing.T) {
	path := "/tmp/test-messages.jsonl"
	ready := captureState("running", path, true) // Zero messages is still ready.
	for _, tc := range []struct {
		name     string
		states   []captureSnapshot
		startErr error
		starts   int
		wantErr  string
	}{
		{name: "already ready", states: []captureSnapshot{ready}},
		{name: "first harvest", states: []captureSnapshot{{}, captureState("starting", path, false), ready}, starts: 1},
		{name: "live poll is not a harvest", states: []captureSnapshot{captureState("running", path, false), ready}},
		{name: "concurrent start", states: []captureSnapshot{{}, captureState("starting", path, false), ready}, startErr: syscall.EWOULDBLOCK, starts: 1},
		{name: "stale state", states: []captureSnapshot{{status: ready.status}, ready}, starts: 1},
		{name: "wrong output", states: []captureSnapshot{captureState("running", "/tmp/another-store.jsonl", true)}, wantErr: "but this query expects"},
		{name: "worker stopped during startup", states: []captureSnapshot{captureState("starting", path, false), {status: background.Status{State: "failed", Detail: "saved login expired"}}}, wantErr: "saved login expired"},
		{name: "older worker", states: []captureSnapshot{{live: true, status: background.Status{State: "running"}}}, wantErr: "updated sessh binary"},
		{name: "start failure", states: []captureSnapshot{{}}, starts: 1, startErr: errors.New("cannot start worker"), wantErr: "cannot start worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeCaptureService{states: tc.states, startErr: tc.startErr}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := []string{"teams", "capture", "--foreground"}
			err := waitForCapture(ctx, s, path, args, func(string) {})
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if s.starts != tc.starts || s.starts > 0 && !reflect.DeepEqual(s.args, args) {
				t.Fatalf("unexpected start: %d, args %q", s.starts, s.args)
			}
			if s.reads != len(tc.states) {
				t.Fatalf("returned before readiness: %d inspections, want %d", s.reads, len(tc.states))
			}
		})
	}
}

func TestWaitForCaptureCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &fakeCaptureService{states: []captureSnapshot{captureState("starting", "/tmp/messages.jsonl", false)}, afterInspect: cancel}
	err := waitForCapture(ctx, s, "/tmp/messages.jsonl", nil, func(string) {})
	if !errors.Is(err, context.Canceled) || s.starts != 0 {
		t.Fatalf("cancellation = %v, starts %d", err, s.starts)
	}
}

func TestMessagesCaptureArguments(t *testing.T) {
	old := plugin.GlobalFlags()
	plugin.BindFlags(&plugin.Flags{NoInherit: true, From: "seed", Profile: "work"})
	t.Cleanup(func() { plugin.BindFlags(old) })
	got := messagesCaptureArgs("work", "/tmp/saved messages.jsonl", true)
	want := []string{"teams", "capture", "--foreground", "--profile=work", "--out=/tmp/saved messages.jsonl", "--no-login", "--no-inherit", "--from=seed", "--no-scroll"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestMessagesOfflineAndValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SESSH_HOME", home)
	t.Setenv("TEAMS_DATA_DIR", "")
	t.Setenv("TEAMS_MESSAGES_FILE", "")
	path := filepath.Join(home, "teams", "messages.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"example\",\"body\":\"Offline fixture\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"messages", "--offline", "-f", "json"}, true},
		{[]string{"msgs", "--source", path, "-f", "json"}, true},
		{[]string{"messages", "--source", path + "-missing"}, false},
		{[]string{"messages", "--since", "invalid-date"}, false},
		{[]string{"messages", "--sort", "invalid-sort"}, false},
		{[]string{"messages", "--format", "invalid-format"}, false},
		{[]string{"messages", "--capture-timeout=0"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := &cobra.Command{Use: "teams", SilenceUsage: true, SilenceErrors: true}
			cmd.AddCommand(messagesCommand())
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tc.args)
			err := cmd.ExecuteContext(context.Background())
			if tc.ok {
				if err != nil || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "Offline fixture") {
					t.Fatalf("query = %q: %v", out.String(), err)
				}
			} else if err == nil || out.Len() != 0 {
				t.Fatalf("invalid query = %q: %v", out.String(), err)
			}
			if _, err := os.Stat(filepath.Join(home, "run")); !os.IsNotExist(err) {
				t.Fatalf("local/invalid query started capture: %v", err)
			}
		})
	}
}
