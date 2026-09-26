package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

const pathBegin = "# >>> sessh PATH >>>"
const pathEnd = "# <<< sessh PATH <<<"

func shellRCPath(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch filepath.Base(os.Getenv("SHELL")) {
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "zsh":
		if dir := os.Getenv("ZDOTDIR"); dir != "" {
			home = dir
		}
		return filepath.Abs(filepath.Join(home, ".zshrc"))
	default:
		return "", fmt.Errorf("cannot select a bash/zsh startup file; pass --rc ~/.bashrc or --rc ~/.zshrc")
	}
}

// Bash login shells read the first existing file in this list, while ordinary
// interactive shells read .bashrc. Set PATH in both, without changing how the
// user's existing login script sources other files.
func shellPathFiles(explicit string) ([]string, error) {
	rc, err := shellRCPath(explicit)
	if err != nil {
		return nil, err
	}
	paths := []string{rc}
	if explicit != "" {
		return paths, nil
	}
	if filepath.Base(os.Getenv("SHELL")) == "zsh" {
		return append(paths, filepath.Join(filepath.Dir(rc), ".zprofile")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
		path := filepath.Join(home, name)
		if _, err := os.Stat(path); err == nil {
			return append(paths, path), nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return append(paths, filepath.Join(home, ".profile")), nil
}

func pathBlock(dir string) string {
	// Quote the directory inside the pattern too: spaces, quotes, $, and shell
	// metacharacters in an installation path must remain literal.
	return pathBegin + "\ncase \"$PATH\" in\n  " + shellQuote(dir) + "|" + shellQuote(dir) + ":*) ;;\n  *) export PATH=" + shellQuote(dir) + ":\"$PATH\" ;;\nesac\n" + pathEnd + "\n"
}

func newSetupCommand() *cobra.Command {
	var rc string
	cmd := &cobra.Command{Use: "setup", Short: "Add sessh to your shell's PATH", Args: cobra.NoArgs,
		Long: `Make the installed sessh available by name in new bash/zsh terminals.

The installer runs this automatically. Existing shell settings are preserved,
and running setup again updates the same entry. Use --rc for a custom startup
file. The current terminal cannot inherit PATH changes from a child process;
setup prints a command to activate them immediately when needed.`,
		RunE: func(c *cobra.Command, _ []string) error {
			binary, err := os.Executable()
			if err != nil {
				return err
			}
			dir := filepath.Dir(binary)
			paths, err := shellPathFiles(rc)
			if err != nil {
				return err
			}
			for _, path := range paths {
				if err := updateShellBlock(path, pathBegin, pathEnd, pathBlock(dir)); err != nil {
					return err
				}
				fmt.Fprintln(c.OutOrStdout(), "Configured sessh on PATH in", path)
			}
			found, _ := exec.LookPath("sessh")
			if found == binary {
				fmt.Fprintln(c.OutOrStdout(), "Ready: sessh --help")
			} else {
				fmt.Fprintln(c.OutOrStdout(), "Open a new terminal, or run this in your current shell:")
				fmt.Fprintln(c.OutOrStdout(), "  export PATH="+shellQuote(dir)+":\"$PATH\"")
				fmt.Fprintln(c.OutOrStdout(), "Then run: sessh --help")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rc, "rc", "", "shell startup file (default: bash/zsh interactive and login files)")
	return cmd
}
