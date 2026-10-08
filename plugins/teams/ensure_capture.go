package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/plugin"
)

type captureService interface {
	Inspect(context.Context) (background.Status, bool, error)
	Start(context.Context, []string) (background.Status, bool, error)
}

func messagesCaptureArgs(profile, path string, noScroll bool) []string {
	args := []string{"teams", "capture", "--foreground", "--profile=" + profile, "--out=" + path, "--no-login"}
	flags := plugin.GlobalFlags()
	if flags.NoInherit {
		args = append(args, "--no-inherit")
	}
	if flags.From != "" {
		args = append(args, "--from="+flags.From)
	}
	if noScroll {
		args = append(args, "--no-scroll")
	}
	return args
}

func (f *queryFlags) ensureMessagesCapture(cmd *cobra.Command) error {
	if !f.autoCapture || f.offline || f.source != "" {
		return nil
	}
	if f.captureTimeout <= 0 {
		return fail(2, "--capture-timeout must be positive")
	}
	var cfg captureConfig
	if err := cfg.resolve(); err != nil {
		return err
	}
	owner := &Teams{}
	profile := plugin.ProfileFor(owner).Name
	spec := plugin.Service("teams capture", owner)
	ctx, cancel := context.WithTimeout(cmd.Context(), f.captureTimeout)
	defer cancel()
	err := waitForCapture(ctx, spec, cfg.messagesPath, messagesCaptureArgs(profile, cfg.messagesPath, f.noScroll), func(msg string) {
		fmt.Fprintln(cmd.ErrOrStderr(), "sessh:", msg)
	})
	if err != nil {
		return fmt.Errorf("Teams capture for profile %q: %w\nCheck sessh teams capture status --profile %q or sessh teams capture logs --profile %q. Use --offline to query saved messages without capture", profile, err, profile, profile)
	}
	return nil
}

// A responding control socket only proves a worker exists. Wait for its first
// complete harvest of this store before querying; a legitimate empty harvest
// is ready too. Cancellation leaves the shared capture running.
func waitForCapture(ctx context.Context, service captureService, path string, args []string, notify func(string)) error {
	observed := false
	notified := false
	var last background.Status
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for a successful capture harvest (%s: %s): %w", last.State, last.Detail, err)
		}
		st, live, err := service.Inspect(ctx)
		if err != nil {
			return err
		}
		last = st
		if !live {
			if observed {
				return fmt.Errorf("capture exited before it was ready (%s): %s", st.State, st.Detail)
			}
			if !notified {
				notify("Starting Teams capture in the background; waiting for its first successful harvest...")
				notified = true
			}
			st, _, err = service.Start(ctx, args)
			if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return fmt.Errorf("starting capture: %w\n%s", err, st.Detail)
			}
			observed = err == nil
		} else {
			observed = true
			switch st.State {
			case "failed", "stopped", "stopping":
				return fmt.Errorf("capture is %s: %s", st.State, st.Detail)
			}
			var progress captureProgress
			if len(st.Progress) > 0 {
				if err := json.Unmarshal(st.Progress, &progress); err != nil {
					return fmt.Errorf("reading capture readiness: %w", err)
				}
				if progress.MessagesFile != "" && filepath.Clean(progress.MessagesFile) != filepath.Clean(path) {
					return fmt.Errorf("capture writes %s, but this query expects %s; use --source %q to read that store or stop and restart capture with the desired output", progress.MessagesFile, path, progress.MessagesFile)
				}
			}
			if st.State == "running" {
				if progress.MessagesFile == "" {
					return fmt.Errorf("this capture worker does not report readiness; stop it and start capture with the updated sessh binary")
				}
				if progress.Ready && !progress.LastHarvest.IsZero() {
					return nil
				}
			}
			if !notified {
				notify("Waiting for Teams capture to complete a successful harvest...")
				notified = true
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
