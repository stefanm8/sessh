package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

const autostartBegin = "# >>> sessh autostart >>>"
const autostartEnd = "# <<< sessh autostart <<<"

func newAutostartCommand() *cobra.Command {
	var rc string
	var noScroll bool
	cmd := &cobra.Command{Use: "autostart", Short: "Start capture or a daemon when opening a shell",
		Long: `Manage one startup entry in your shell configuration.

'sessh autostart enable teams' starts Teams capture whenever an interactive
shell opens. Other plugins start their browser daemon. Already-running workers
are reused and keep running after the terminal closes. Log in once first.

The entry uses .zshrc for zsh and .bashrc for bash. Bash login shells must source
.bashrc. This starts work when opening a shell, not at machine boot.`,
	}
	cmd.PersistentFlags().StringVar(&rc, "rc", "", "shell startup file (default: ~/.bashrc or ${ZDOTDIR:-$HOME}/.zshrc)")
	resolveRC := func() (string, error) { return shellRCPath(rc) }

	enable := &cobra.Command{Use: "enable <plugin>", Short: "Add or update the shell startup entry", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		owner, err := resolvePluginArg(args)
		if err != nil {
			return err
		}
		path, err := resolveRC()
		if err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		root, err := filepath.Abs(session.Root())
		if err != nil {
			return err
		}
		profile := plugin.ProfileFor(owner).Name
		start := []string{binary, "daemon", "start", owner.Name()}
		if owner.Name() == "teams" {
			start = []string{binary, "teams", "capture", "start"}
		}
		start = append(start, "--profile="+profile)
		if noScroll {
			if owner.Name() != "teams" {
				return fmt.Errorf("--no-scroll only applies to Teams capture")
			}
			start = append(start, "--no-scroll")
		}
		words := []string{"env", shellQuote("SESSH_HOME=" + root)}
		for _, word := range start {
			words = append(words, shellQuote(word))
		}
		block := autostartBegin + "\n# Start once after login; subsequent shells reuse the same worker.\ncase $- in\n  *i*)\n    if [ -x " + shellQuote(binary) + " ] && [ -d " + shellQuote(filepath.Join(root, "profiles", profile, "Default")) + " ]; then\n      (nohup " + strings.Join(words, " ") + " </dev/null >/dev/null 2>&1 &)\n    fi\n    ;;\nesac\n" + autostartEnd + "\n"
		if err := updateAutostart(path, block); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Shell startup enabled in", path)
		fmt.Fprintln(c.OutOrStdout(), "Open a new shell after signing in. Disable with: sessh autostart disable --rc", shellQuote(path))
		return nil
	}}
	enable.Flags().BoolVar(&noScroll, "no-scroll", false, "capture cached Teams messages without opening chats or marking them read")
	cmd.AddCommand(enable)
	cmd.AddCommand(&cobra.Command{Use: "disable", Short: "Remove the shell startup entry (does not stop a running worker)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		path, err := resolveRC()
		if err != nil {
			return err
		}
		if err := updateAutostart(path, ""); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Shell startup disabled in", path)
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Show the configured shell startup entry", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		path, err := resolveRC()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		_, block, err := splitAutostart(string(data))
		if err != nil {
			return err
		}
		if block == "" {
			fmt.Fprintln(c.OutOrStdout(), "Shell startup is disabled in", path)
		} else {
			fmt.Fprintln(c.OutOrStdout(), path+":\n"+block)
		}
		return nil
	}})
	return cmd
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Only our explicitly marked block is editable. Refuse malformed/duplicated
// markers rather than risk removing unrelated shell configuration.
func splitAutostart(data string) (string, string, error) {
	return splitShellBlock(data, autostartBegin, autostartEnd)
}

func splitShellBlock(data, beginMarker, endMarker string) (string, string, error) {
	begin, end := strings.Index(data, beginMarker), strings.Index(data, endMarker)
	if begin < 0 && end < 0 {
		return data, "", nil
	}
	if begin < 0 || end < begin || strings.Count(data, beginMarker) != 1 || strings.Count(data, endMarker) != 1 {
		return "", "", fmt.Errorf("malformed %s block; fix its markers before retrying", beginMarker)
	}
	end += len(endMarker)
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return data[:begin] + data[end:], data[begin:end], nil
}

func updateAutostart(path, block string) error {
	return updateShellBlock(path, autostartBegin, autostartEnd, block)
}

func updateShellBlock(path, beginMarker, endMarker, block string) error {
	// Follow a dotfile symlink so we keep the user's dotfile management intact.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	base, _, err := splitShellBlock(string(data), beginMarker, endMarker)
	if err != nil {
		return err
	}
	if block != "" && base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	result := base + block
	if result == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sessh-rc-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(result); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
