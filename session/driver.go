package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mxschmitt/playwright-go"
)

// DriverVersion is the Playwright driver playwright-go expects. It must match
// the `playwright-core` package we borrow when one is available locally.
const DriverVersion = "1.61.1"

var (
	pwOnce sync.Once
	pwInst *playwright.Playwright
	pwErr  error
)

// playwrightRun starts (once per process) the Playwright driver.
//
// Use an installed driver, or link an exact-version playwright-core package
// and the Node executable into the driver cache. Setup is explicit; running a
// command never installs packages or browsers automatically.
func playwrightRun() (*playwright.Playwright, error) {
	pwOnce.Do(func() {
		if err := ensureDriver(); err != nil {
			pwErr = err
			return
		}
		pwInst, pwErr = playwright.Run()
	})
	return pwInst, pwErr
}

func driverDir() string {
	if v := os.Getenv("PLAYWRIGHT_DRIVER_PATH"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "ms-playwright-go", DriverVersion)
	case "windows":
		return filepath.Join(home, "AppData", "Local", "ms-playwright-go", DriverVersion)
	default:
		return filepath.Join(home, ".cache", "ms-playwright-go", DriverVersion)
	}
}

func ensureDriver() error {
	dir := driverDir()
	if dir == "" {
		return fmt.Errorf("could not determine the Playwright driver directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "package", "cli.js")); err == nil {
		return nil
	}
	pkg := findPlaywrightCore()
	if pkg == "" {
		return fmt.Errorf(
			"no Playwright driver at %s and no local playwright-core %s to borrow.\n"+
				"Install one with: npm i -g playwright-core@%s  (or set PLAYWRIGHT_DRIVER_PATH)",
			dir, DriverVersion, DriverVersion)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is required to run the Playwright driver: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "package"))
	if err := os.Symlink(pkg, filepath.Join(dir, "package")); err != nil {
		return fmt.Errorf("linking playwright driver: %w", err)
	}
	_ = os.Remove(filepath.Join(dir, "node"))
	if err := os.Symlink(node, filepath.Join(dir, "node")); err != nil {
		return fmt.Errorf("linking node: %w", err)
	}
	return nil
}

// findPlaywrightCore looks for an installed playwright-core of the exact
// version the Go bindings speak, near the executable or working directory,
// then in npm's global package directory.
func findPlaywrightCore() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(exe))
		candidates = append(candidates, filepath.Join(root, "node_modules", "playwright-core"))
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; {
			candidates = append(candidates, filepath.Join(dir, "node_modules", "playwright-core"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
		candidates = append(candidates, filepath.Join(strings.TrimSpace(string(out)), "playwright-core"))
	}
	for _, c := range candidates {
		if versionOf(c) == DriverVersion {
			return c
		}
	}
	return ""
}

func versionOf(pkgDir string) string {
	b, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return ""
	}
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return ""
	}
	return p.Version
}
