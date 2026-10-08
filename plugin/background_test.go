package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/internal/background"
)

func TestBackgroundArgs(t *testing.T) {
	root := &cobra.Command{Use: "sessh"}
	root.PersistentFlags().String("profile", "", "")
	capture := &cobra.Command{Use: "capture"}
	capture.PersistentFlags().Bool("no-scroll", false, "")
	capture.PersistentFlags().String("out", "", "")
	capture.Flags().BoolP("background", "d", false, "")
	teams := &cobra.Command{Use: "teams"}
	start := &cobra.Command{Use: "start", RunE: func(c *cobra.Command, _ []string) error {
		got := BackgroundArgs(c, nil)
		want := []string{"teams", "capture", "--no-scroll=true", "--out=/tmp/a file.jsonl", "--profile=work"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args = %q, want %q", got, want)
		}
		return nil
	}}
	root.AddCommand(teams)
	teams.AddCommand(capture)
	capture.AddCommand(start)
	root.SetArgs([]string{"--profile=work", "teams", "capture", "--no-scroll", "start", "--out=/tmp/a file.jsonl"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	capture.RunE = func(c *cobra.Command, _ []string) error {
		for _, a := range BackgroundArgs(c, nil) {
			if a == "--background=true" {
				t.Fatal("recursive background flag retained")
			}
		}
		return nil
	}
	root.SetArgs([]string{"teams", "capture", "-d"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundArgsRunsCaptureInChild(t *testing.T) {
	for _, args := range [][]string{
		{"teams", "capture"},
		{"teams", "capture", "-d"},
		{"teams", "capture", "--foreground=false"},
		{"teams", "capture", "start"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := &cobra.Command{Use: "sessh"}
			teams := &cobra.Command{Use: "teams"}
			capture := &cobra.Command{Use: "capture"}
			capture.PersistentFlags().Bool("foreground", false, "")
			capture.Flags().BoolP("background", "d", false, "")
			run := func(cmd *cobra.Command, _ []string) error {
				want := []string{"teams", "capture", "--foreground=true"}
				if got := BackgroundArgs(cmd, nil); !reflect.DeepEqual(got, want) {
					t.Fatalf("worker args = %q, want %q", got, want)
				}
				return nil
			}
			capture.RunE = run
			capture.AddCommand(&cobra.Command{Use: "start", RunE: run})
			teams.AddCommand(capture)
			root.AddCommand(teams)
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serviceTestSpec(t *testing.T) background.Spec {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sessh-watch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return background.Spec{Dir: dir, Key: "capture:test", Name: "test capture"}
}

func serviceTestCommand(spec background.Spec, out *bytes.Buffer, args ...string) *cobra.Command {
	cmd := &cobra.Command{Use: "capture", SilenceUsage: true, SilenceErrors: true}
	cmd.SetOut(out)
	AddServiceControls(cmd, cobra.NoArgs, func([]string) (background.Spec, error) { return spec, nil })
	cmd.SetArgs(args)
	return cmd
}

func TestServiceStatusJSONAndFailures(t *testing.T) {
	spec := serviceTestSpec(t)
	if err := spec.Run(context.Background(), func(context.Context, func(string, string)) error {
		return errors.New("saved login expired")
	}); err == nil {
		t.Fatal("expected worker failure")
	}
	var out bytes.Buffer
	err := serviceTestCommand(spec, &out, "status", "--json").ExecuteContext(context.Background())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("status error = %v, want exit 1", err)
	}
	var st background.Status
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("invalid JSON status: %s: %v", out.String(), err)
	}
	if st.State != "failed" || st.Detail != "saved login expired" || st.PID == 0 || st.Started.IsZero() {
		t.Fatalf("lost worker diagnostics: %+v", st)
	}
}

type statusObserver struct {
	bytes.Buffer
	onWrite func()
}

func (w *statusObserver) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b)
	w.onWrite()
	return n, err
}

func TestServiceMonitorDoesNotStopWorker(t *testing.T) {
	spec := serviceTestSpec(t)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- spec.Run(workerCtx, func(ctx context.Context, report func(string, string)) error {
			report("running", "Messages: 3 total")
			close(ready)
			<-ctx.Done()
			return nil
		})
	}()
	t.Cleanup(func() { stopWorker(); <-done })
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	for _, args := range [][]string{{"monitor", "--json"}, {"status", "--watch", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out := &statusObserver{}
			frames := 0
			out.onWrite = func() {
				frames++
				if frames == 2 {
					cancel()
				}
			}
			cmd := serviceTestCommand(spec, &bytes.Buffer{}, append(args, "--interval=1ms")...)
			cmd.SetOut(out)
			if err := cmd.ExecuteContext(ctx); err != nil {
				t.Fatal(err)
			}
			if frames != 2 {
				t.Fatalf("monitor produced %d frames", frames)
			}
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var st background.Status
				if err := json.Unmarshal([]byte(line), &st); err != nil || st.State != "running" || st.Detail != "Messages: 3 total" {
					t.Fatalf("monitor frame = %s: %v", line, err)
				}
			}
			if st, live, err := spec.Inspect(context.Background()); err != nil || !live || st.State != "running" {
				t.Fatalf("monitor stopped worker: %+v, live=%v: %v", st, live, err)
			}
		})
	}
}

func TestMonitorContinuesAfterFailure(t *testing.T) {
	spec := serviceTestSpec(t)
	_ = spec.Run(context.Background(), func(context.Context, func(string, string)) error { return errors.New("offline") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := &statusObserver{}
	frames := 0
	out.onWrite = func() {
		frames++
		if frames == 1 {
			// Simulate a subsequent run ending cleanly while monitoring continues.
			if err := spec.Run(context.Background(), func(context.Context, func(string, string)) error { return nil }); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
	}
	cmd := serviceTestCommand(spec, &bytes.Buffer{}, "monitor", "--json", "--interval=1ms")
	cmd.SetOut(out)
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if frames != 2 || !strings.Contains(out.String(), `"state":"failed"`) || !strings.Contains(out.String(), `"state":"stopped"`) {
		t.Fatalf("monitor did not observe recovery: %s", out.String())
	}
}

func TestMonitorRejectsInvalidInterval(t *testing.T) {
	spec := serviceTestSpec(t)
	for _, interval := range []string{"0", "-1s"} {
		var out bytes.Buffer
		err := serviceTestCommand(spec, &out, "monitor", "--interval="+interval).ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), "--interval must be positive") || out.Len() != 0 {
			t.Fatalf("interval %s: output=%q, error=%v", interval, out.String(), err)
		}
	}
}
