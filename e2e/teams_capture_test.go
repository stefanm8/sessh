//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stefanm8/sessh/internal/background"
)

// Real Chromium and the real re-exec path, using only a local IndexedDB
// fixture. No account, saved login, or Microsoft endpoint is needed.
func teamsCaptureEnv(t *testing.T) *env {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Teams capture fixture</title><script>
const rc = indexedDB.open('fixture:replychain-manager:test', 1);
rc.onupgradeneeded = () => rc.result.createObjectStore('replychains');
rc.onsuccess = () => {
  const tx = rc.result.transaction('replychains', 'readwrite');
  tx.objectStore('replychains').put({messageMap: {example: {
    id: 'fixture-message', messageType: 'Text', content: 'Synthetic capture',
    conversationId: '19:fixture@thread.v2'
  }}}, 'example');
  tx.oncomplete = () => rc.result.close();
};
const conv = indexedDB.open('fixture:conversation-manager:test', 1);
conv.onupgradeneeded = () => conv.result.createObjectStore('conversations');
conv.onsuccess = () => {
  const tx = conv.result.transaction('conversations', 'readwrite');
  tx.objectStore('conversations').put({id: '19:fixture@thread.v2', type: 'chat',
    threadProperties: {topic: 'Fixture chat'}}, 'example');
  tx.oncomplete = () => conv.result.close();
};
</script>`)
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t, nil, nil)
	e.vars = append(e.vars, "TEAMS_URL="+srv.URL, "TEAMS_DATA_DIR=", "TEAMS_MESSAGES_FILE=", "TEAMS_CONVERSATIONS_FILE=")
	return e
}

func TestTeamsCaptureDaemon(t *testing.T) {
	e := teamsCaptureEnv(t)
	profile := "capture-test"
	args := []string{"teams", "capture", "--profile", profile, "--no-inherit", "--no-scroll", "--poll-interval=1s", "--idb-interval=5s"}
	started := e.start(args...)
	started.mustExit(20 * time.Second)
	started.expect(0, "Started in the background", "")
	spec := background.Spec{Dir: e.home + "/run", Key: "teams capture:" + profile, Name: "test capture"}
	var st background.Status
	waitFor(t, "capture has harvested and polled", 60*time.Second, func() bool {
		var live bool
		var err error
		st, live, err = spec.Inspect(context.Background())
		if err != nil || st.State == "failed" {
			t.Fatalf("capture failed: %+v: %v", st, err)
		}
		return live && st.State == "running" && strings.Contains(st.Detail, "1 total, 1 new this run") && !strings.Contains(st.Detail, "not yet")
	})
	pid := st.PID
	if !st.Detached || pid == started.cmd.Process.Pid || !strings.Contains(strings.Join(st.Args, " "), "--foreground=true") {
		t.Fatalf("capture did not detach into its worker: %+v", st)
	}
	if sid, err := syscall.Getsid(pid); err != nil || sid != pid {
		t.Fatalf("worker session = %d, want %d: %v", sid, pid, err)
	}
	e.run(args...).expect(0, "Already running.", "")
	monitor := e.start("teams", "capture", "monitor", "--profile", profile, "--json", "--interval=100ms")
	waitFor(t, "monitor prints multiple snapshots", 10*time.Second, func() bool {
		return strings.Count(monitor.stdout.String(), "\n") >= 2
	})
	if err := monitor.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	monitor.mustExit(5 * time.Second)
	monitor.expect(0, `"state":"running"`, "")
	for _, line := range strings.Split(strings.TrimSpace(monitor.stdout.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("invalid monitor JSON: %s", line)
		}
	}
	if current, live, err := spec.Inspect(context.Background()); err != nil || !live || current.PID != pid {
		t.Fatalf("monitor interrupted capture: %+v, live=%v: %v", current, live, err)
	}
	e.run("teams", "capture", "logs", "--profile", profile).expect(0, "idb 1 new message", "")
	e.run("teams", "capture", "stop", "--profile", profile).expect(0, "Stopped", "")
	e.run("teams", "capture", "status", "--profile", profile, "--json").expect(0, `"state":"stopped"`, "")
}

func TestTeamsMessagesStartsCapture(t *testing.T) {
	e := teamsCaptureEnv(t)
	e.vars = append(e.vars, "TEAMS_DATA_DIR="+e.home+"/custom messages")
	profile := "messages-test"
	args := []string{"teams", "messages", "--profile", profile, "--no-inherit", "--no-scroll", "--capture-timeout=60s", "--format=json"}
	// Two first readers must wait for the same worker and its initial harvest,
	// rather than treating the freshly created but empty file as ready.
	first, second := e.start(args...), e.start(args...)
	for _, c := range []*command{first, second} {
		c.mustExit(70 * time.Second)
		c.expect(0, "Synthetic capture", "")
		if !json.Valid([]byte(c.stdout.String())) {
			t.Fatalf("startup polluted message JSON: %s", c.stdout.String())
		}
	}
	spec := background.Spec{Dir: e.home + "/run", Key: "teams capture:" + profile, Name: "test capture"}
	st, live, err := spec.Inspect(context.Background())
	if err != nil || !live || !st.Detached || st.State != "running" {
		t.Fatalf("capture not left running: %+v, live=%v: %v", st, live, err)
	}
	var progress struct {
		Ready        bool   `json:"ready"`
		MessagesFile string `json:"messages_file"`
		Messages     int    `json:"messages"`
	}
	if err := json.Unmarshal(st.Progress, &progress); err != nil || !progress.Ready || progress.Messages != 1 || progress.MessagesFile != e.home+"/custom messages/messages.jsonl" {
		t.Fatalf("incorrect capture readiness: %s: %v", st.Progress, err)
	}
	e.run(args...).expect(0, "Synthetic capture", "")
	if current, live, err := spec.Inspect(context.Background()); err != nil || !live || current.PID != st.PID {
		t.Fatalf("query replaced running capture: %+v: %v", current, err)
	}
	e.run("teams", "capture", "stop", "--profile", profile).expect(0, "Stopped", "")
	// Cached data must not bypass startup. This read restarts capture and
	// waits for a new harvest without duplicating the saved message.
	restarted := e.run(args...)
	restarted.expect(0, "Synthetic capture", "Starting Teams capture")
	if strings.Count(restarted.stdout.String(), "fixture-message") != 1 {
		t.Fatalf("restart duplicated stored messages: %s", restarted.stdout.String())
	}
	if current, live, err := spec.Inspect(context.Background()); err != nil || !live || current.PID == st.PID {
		t.Fatalf("query failed to restart capture: %+v: %v", current, err)
	}
}
