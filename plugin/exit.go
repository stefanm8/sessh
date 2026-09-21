package plugin

import "fmt"

// ExitError lets a command choose the process exit code without knowing
// anything about the process. `bitbucket pr checks` is a CI gate and follows
// gh's 0/1/8 convention this way.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Exit returns an error carrying an exit code, or nil for 0.
func Exit(code int) error {
	if code == 0 {
		return nil
	}
	return &ExitError{Code: code}
}
