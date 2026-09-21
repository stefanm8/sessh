package installtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/session"
)

const fixtureVersion = "1.2.3"
const fixtureBinary = "#!/bin/sh\necho 'sessh version 1.2.3'\n"

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (dir, archive string) {
	t.Helper()
	dir = t.TempDir()
	prefix := "sessh_" + fixtureVersion + "_" + runtime.GOOS + "_" + runtime.GOARCH
	archive = prefix + ".tar.gz"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"sessh":            fixtureBinary,
		"setup-browser.sh": "#!/bin/sh\nprintf ready > \"$BROWSER_MARKER\"\n",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: prefix + "/" + name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, archive), buf.String(), 0o644)
	write(t, filepath.Join(dir, "sessh_"+fixtureVersion+"_checksums.txt"), fmt.Sprintf("%x  %s\n", sha256.Sum256(buf.Bytes()), archive), 0o644)
	return dir, archive
}

func run(t *testing.T, script string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(projectRoot(t), script)}, args...)...)
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := env[key]; !replaced && !strings.HasPrefix(key, "SESSH_") && !strings.HasPrefix(key, "PLAYWRIGHT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstall(t *testing.T) {
	dir, _ := fixture(t)
	home := t.TempDir()
	binDir := filepath.Join(home, "bin with spaces")
	args := []string{"--from-dir", dir, "--version", "v" + fixtureVersion, "--bin-dir", binDir}
	for i := 0; i < 2; i++ { // Reinstallation replaces a binary atomically.
		if out, err := run(t, "install.sh", map[string]string{"HOME": home}, args...); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	body, err := os.ReadFile(filepath.Join(binDir, "sessh"))
	if err != nil || string(body) != fixtureBinary {
		t.Fatalf("installed %q, error %v", body, err)
	}
	info, err := os.Stat(filepath.Join(binDir, "sessh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary permissions: %v, %v", info, err)
	}
	entries, err := os.ReadDir(binDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("installer left temporary files: %v, %v", entries, err)
	}
	if out, err := run(t, "install.sh", map[string]string{"HOME": home}, "--from-dir", dir, "--version", fixtureVersion); err != nil {
		t.Fatalf("default directory: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/bin/sessh")); err != nil {
		t.Fatal(err)
	}
}

func TestBadDownloadPreservesInstallation(t *testing.T) {
	for _, kind := range []string{"corrupt", "missing-checksum", "duplicate-checksum"} {
		t.Run(kind, func(t *testing.T) {
			dir, archive := fixture(t)
			manifest := filepath.Join(dir, "sessh_"+fixtureVersion+"_checksums.txt")
			switch kind {
			case "corrupt":
				write(t, filepath.Join(dir, archive), "bad download", 0o644)
			case "missing-checksum":
				write(t, manifest, "", 0o644)
			case "duplicate-checksum":
				b, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				write(t, manifest, string(b)+string(b), 0o644)
			}
			binDir := t.TempDir()
			write(t, filepath.Join(binDir, "sessh"), "keep me", 0o755)
			out, err := run(t, "install.sh", map[string]string{"HOME": t.TempDir()}, "--from-dir", dir, "--version", fixtureVersion, "--bin-dir", binDir)
			if err == nil {
				t.Fatalf("accepted bad download: %s", out)
			}
			b, _ := os.ReadFile(filepath.Join(binDir, "sessh"))
			if string(b) != "keep me" {
				t.Fatal("existing binary was changed")
			}
		})
	}
}

func TestInstallBrowserOptIn(t *testing.T) {
	dir, _ := fixture(t)
	home := t.TempDir()
	stubs := filepath.Join(home, "stubs")
	for _, name := range []string{"node", "npm"} {
		write(t, filepath.Join(stubs, name), "#!/bin/sh\nexit 0\n", 0o755)
	}
	marker := filepath.Join(home, "browser-ran")
	env := map[string]string{"HOME": home, "PATH": stubs + string(os.PathListSeparator) + os.Getenv("PATH"), "BROWSER_MARKER": marker}
	args := []string{"--from-dir", dir, "--version", fixtureVersion}
	if out, err := run(t, "install.sh", env, args...); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("browser setup ran without opt-in")
	}
	if out, err := run(t, "install.sh", env, append(args, "--with-browser")...); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("browser setup did not run")
	}
}

func TestInstallGitHubTransport(t *testing.T) {
	dir, _ := fixture(t)
	home := t.TempDir()
	stubs := filepath.Join(home, "stubs")
	write(t, filepath.Join(stubs, "gh"), `#!/bin/sh
case "$1 $2" in
  'auth status') exit 0 ;;
  'release view') echo v1.2.3 ;;
  'release download')
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --dir ]; then shift; dest=$1; fi
      shift
    done
    cp "$FIXTURE_DIR/"* "$dest/" ;;
  *) exit 1 ;;
esac
`, 0o755)
	env := map[string]string{"HOME": home, "PATH": stubs + string(os.PathListSeparator) + os.Getenv("PATH"), "FIXTURE_DIR": dir}
	if out, err := run(t, "install.sh", env); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/bin/sessh")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRejectsInvalidInput(t *testing.T) {
	dir, _ := fixture(t)
	for _, args := range [][]string{
		{"--unknown"}, {"--version"}, {"--from-dir", dir},
		{"--from-dir", dir, "--version", "../../elsewhere"},
		{"--from-dir", dir, "--version", "v1.0.0\nbad"},
	} {
		if out, err := run(t, "install.sh", map[string]string{"HOME": t.TempDir()}, args...); err == nil {
			t.Fatalf("accepted %v: %s", args, out)
		}
	}
}

func TestBrowserSetup(t *testing.T) {
	home := t.TempDir()
	stubs := filepath.Join(home, "stubs")
	driver := filepath.Join(home, "driver with spaces")
	write(t, filepath.Join(stubs, "node"), `#!/bin/sh
if [ "$1" = -p ]; then echo 1.61.1; exit 0; fi
printf '%s\n' "$*" >> "$NODE_LOG"
`, 0o755)
	write(t, filepath.Join(stubs, "npm"), `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_LOG"
while [ "$#" -gt 0 ]; do
  if [ "$1" = --prefix ]; then shift; prefix=$1; fi
  shift
done
mkdir -p "$prefix/node_modules/playwright-core"
echo '{"version":"1.61.1"}' > "$prefix/node_modules/playwright-core/package.json"
echo '// fixture' > "$prefix/node_modules/playwright-core/cli.js"
`, 0o755)
	env := map[string]string{"HOME": home, "PATH": stubs + string(os.PathListSeparator) + os.Getenv("PATH"), "PLAYWRIGHT_DRIVER_PATH": driver, "NODE_LOG": filepath.Join(home, "node.log"), "NPM_LOG": filepath.Join(home, "npm.log")}
	for i := 0; i < 2; i++ {
		if out, err := run(t, "scripts/setup-browser.sh", env); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	npmLog, _ := os.ReadFile(env["NPM_LOG"])
	if strings.Count(string(npmLog), "playwright-core@1.61.1") != 1 || !strings.Contains(string(npmLog), "--ignore-scripts") {
		t.Fatalf("unexpected npm install: %s", npmLog)
	}
	nodeLog, _ := os.ReadFile(env["NODE_LOG"])
	if strings.Count(string(nodeLog), "install chromium") != 2 {
		t.Fatalf("browser install: %s", nodeLog)
	}
	if _, err := os.Readlink(filepath.Join(driver, "package")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(stubs, "node"), "#!/bin/sh\necho 0.0.0\n", 0o755)
	if out, err := run(t, "scripts/setup-browser.sh", env); err == nil {
		t.Fatalf("replaced incompatible existing driver: %s", out)
	}
}

func TestBrowserVersionMatchesBinding(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(projectRoot(t), "scripts/setup-browser.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "PLAYWRIGHT_VERSION="+session.DriverVersion+"\n") {
		t.Fatal("browser setup and Go binding driver versions differ")
	}
}

func TestInstallCurlTransport(t *testing.T) {
	dir, _ := fixture(t)
	home := t.TempDir()
	stubs := filepath.Join(home, "stubs")
	write(t, filepath.Join(stubs, "gh"), "#!/bin/sh\nexit 1\n", 0o755)
	write(t, filepath.Join(stubs, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) shift; dest=$1 ;;
    https://github.com/stefanm8/sessh/releases/*) url=$1 ;;
  esac
  shift
done
if [ "$dest" = /dev/null ]; then
  printf 'https://github.com/stefanm8/sessh/releases/tag/v1.2.3'
else
  cp "$FIXTURE_DIR/${url##*/}" "$dest"
fi
`, 0o755)
	env := map[string]string{"HOME": home, "PATH": stubs + string(os.PathListSeparator) + os.Getenv("PATH"), "FIXTURE_DIR": dir}
	if out, err := run(t, "install.sh", env); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/bin/sessh")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallUnsupportedPlatform(t *testing.T) {
	dir, _ := fixture(t)
	home := t.TempDir()
	stubs := filepath.Join(home, "stubs")
	write(t, filepath.Join(stubs, "uname"), "#!/bin/sh\necho Windows\n", 0o755)
	env := map[string]string{"HOME": home, "PATH": stubs + string(os.PathListSeparator) + os.Getenv("PATH")}
	out, err := run(t, "install.sh", env, "--from-dir", dir, "--version", fixtureVersion)
	if err == nil || !strings.Contains(out, "Supported systems") {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatal("unsupported platform touched the install directory")
	}
}
