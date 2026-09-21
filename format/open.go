package format

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenInBrowser opens a URL in the user's default browser — gh's `--web`
// behavior. Nothing about this touches a sessh profile; it is the ordinary
// desktop browser.
func OpenInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println(url)
		return fmt.Errorf("could not open a browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	fmt.Println(Gray("Opening ") + url + Gray(" in your browser..."))
	return nil
}
