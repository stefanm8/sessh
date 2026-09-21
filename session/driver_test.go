package session

import (
	"testing"

	"github.com/mxschmitt/playwright-go"
)

func TestDriverVersionMatchesGoBinding(t *testing.T) {
	t.Setenv("PLAYWRIGHT_DRIVER_PATH", t.TempDir())
	driver, err := playwright.NewDriver()
	if err != nil {
		t.Fatal(err)
	}
	if driver.Version != DriverVersion {
		t.Fatalf("Go binding needs Playwright %s; update DriverVersion and browser setup from %s", driver.Version, DriverVersion)
	}
}
