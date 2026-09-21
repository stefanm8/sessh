package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// capture.go is the port of teams-cli/capture/capture.js onto the session hub.
//
// The shape of the Node original survives — id-then-content dedupe, an
// append-only messages log beside a rewritten conversation sidecar, an
// IndexedDB harvest on a timer, a defensive normalizer — but the *plumbing*
// inverts. capture.js owned a Playwright Page and hung callbacks off it. Here a
// plugin gets only serializable methods, because the same Session is served
// in-process and over a unix socket to whoever holds the profile.
// Callbacks cannot cross that boundary, so the live capture
// and the auto-scroller live inside the page (capturejs.go) and this file polls
// them. That is the one architectural deviation, and it is deliberate.
//
// This command is also why AcquireOptions.Serve exists: it holds a browser
// profile for hours, and serving it on a socket is what turns the old
// SingletonLock collision into other sessh commands quietly sharing the session.

const (
	// Teams populates its IndexedDB connections as the app boots; harvesting
	// before it settles reads a cold cache and finds nothing.
	idbSettleDelay = 10 * time.Second

	// Consecutive Eval failures that mean the page (or the whole browser) is
	// gone rather than merely busy.
	maxEvalFailures = 5

	// A live payload larger than this is not worth shipping across the Eval
	// bridge; IndexedDB carries the same messages anyway.
	maxLivePayloadBytes = 1 << 20
	// Buffered payloads awaiting a drain. Bounded so a stalled poller costs the
	// page a fixed amount of memory.
	maxLiveEvents = 64
)

// ---------------------------------------------------------------------------
// Command
// ---------------------------------------------------------------------------

func captureCommand(t *Teams) *cobra.Command {
	var cfg captureConfig

	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture Teams messages into the local store",
		Long: `Run the Teams capture daemon.

It holds the Teams browser profile open and serves it on a socket, so other sessh
commands share this session instead of failing to launch. On a timer it reads
Teams' own IndexedDB — the only place a complete message body reliably lands,
since live WebSocket traffic carries notification envelopes — and appends what
is new to messages.jsonl, keeping conversations.jsonl in step.

Runs until interrupted. Ctrl+C is a clean shutdown.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			return runCapture(ctx, t, cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.dataDir, "data-dir", "", "directory holding the capture files (default $TEAMS_DATA_DIR or ~/.sessh/teams)")
	f.StringVar(&cfg.out, "out", "", "messages file to append to (default <data-dir>/messages.jsonl)")
	f.BoolVar(&cfg.headed, "headed", false, "show the browser window (needs the profile to itself)")
	f.BoolVar(&cfg.noScroll, "no-scroll", false, "do not open and scroll conversations to widen the harvest")
	f.DurationVar(&cfg.idbInterval, "idb-interval", 60*time.Second, "how often to re-read Teams' IndexedDB")
	f.DurationVar(&cfg.pollInterval, "poll-interval", 5*time.Second, "how often to drain the in-page live capture")
	f.BoolVar(&cfg.once, "once", false, "harvest IndexedDB once and exit")
	f.BoolVar(&cfg.verbose, "verbose", false, "log every captured message")
	return cmd
}

type captureConfig struct {
	dataDir      string
	out          string
	headed       bool
	noScroll     bool
	idbInterval  time.Duration
	pollInterval time.Duration
	once         bool
	verbose      bool

	messagesPath  string
	convPath      string
	scrollEnabled bool
	scroll        scrollerConfig
	intercept     interceptConfig
}

// resolve applies the documented precedence — flag, then environment, then the
// shared default — for every path, and keeps the env knobs of the Node capture
// working where they were public.
func (c *captureConfig) resolve() error {
	dir := c.dataDir
	if dir == "" {
		dir = DataDirDefault() // reads TEAMS_DATA_DIR
	}

	switch {
	case c.out != "":
		c.messagesPath = c.out
	case os.Getenv("TEAMS_MESSAGES_FILE") != "":
		c.messagesPath = os.Getenv("TEAMS_MESSAGES_FILE")
	default:
		c.messagesPath = filepath.Join(dir, "messages.jsonl")
	}
	abs, err := filepath.Abs(c.messagesPath)
	if err != nil {
		return fmt.Errorf("resolving the messages file: %w", err)
	}
	c.messagesPath = abs

	if v := os.Getenv("TEAMS_CONVERSATIONS_FILE"); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			c.convPath = abs
		} else {
			c.convPath = v
		}
	} else {
		c.convPath = ConversationsPathFor(c.messagesPath)
	}

	if c.pollInterval < time.Second {
		c.pollInterval = time.Second
	}
	if c.idbInterval < 5*time.Second {
		c.idbInterval = 5 * time.Second
	}

	c.scrollEnabled = !c.noScroll && os.Getenv("SCROLL_DISABLE") != "1"
	c.scroll = scrollerConfigFromEnv()
	c.intercept = interceptConfig{
		Version:    captureShimVersion,
		EndpointRe: captureEndpointRe,
		HintRe:     captureHintRe,
		MaxEvents:  maxLiveEvents,
		MaxBytes:   maxLivePayloadBytes,
	}
	return nil
}

type interceptConfig struct {
	Version    string `json:"version"`
	EndpointRe string `json:"endpointRe"`
	HintRe     string `json:"hintRe"`
	MaxEvents  int    `json:"maxEvents"`
	MaxBytes   int    `json:"maxBytes"`
}

// captureSelectors is DEFAULT_SELECTORS from scroller.js. It lives on the Go
// side so the Teams markup can be retargeted without touching the shim, which
// is the same reason the Node driver kept it configurable: these are candidates
// tried in order, and Teams' markup shifts.
type captureSelectors struct {
	List        []string `json:"list"`
	MessagePane []string `json:"messagePane"`
	Avatar      string   `json:"avatar"`
}

// Verified against Teams v2 (teams.cloud.microsoft, Fluent UI v9) on
// 2026-07-07: the left rail is one big role="tree", and real conversations are
// aria-level=2 leaves *with an avatar* — nav rows (Mentions/Discover/Drafts)
// are level-2 but avatar-less, and group headers (Chats/Favorites) are
// expandable level-1, where a click collapses the list instead of opening a
// chat.
var defaultSelectors = captureSelectors{
	List: []string{
		`div[role="tree"] [role="treeitem"][aria-level="2"]`,
		`div[role="tree"] [role="treeitem"]`,
		`[role="treeitem"][aria-level="2"]`,
		`[role="treeitem"]`,
	},
	MessagePane: []string{
		`[data-tid="message-pane-list-viewport"]`,
		`[data-tid="message-pane-list-runway"]`,
		`[role="treegrid"].fui-MessageSlice`,
		`[data-tid="messages-list"]`,
		`div[role="log"]`,
	},
	Avatar: `img, [data-tid="PersonaAvatar"], [class*="Avatar" i]`,
}

// scrollerConfig is DEFAULTS from scroller.js, every knob env-overridable.
type scrollerConfig struct {
	Version                  string           `json:"version"`
	Selectors                captureSelectors `json:"selectors"`
	FilterConversations      bool             `json:"filterConversations"`
	IntervalMS               int64            `json:"intervalMs"`
	JitterMS                 int64            `json:"jitterMs"`
	StartDelayMS             int64            `json:"startDelayMs"`
	ScrollSteps              int64            `json:"scrollSteps"`
	ScrollDelayMS            int64            `json:"scrollDelayMs"`
	SettleAfterOpenMS        int64            `json:"settleAfterOpenMs"`
	PerConversationTimeoutMS int64            `json:"perConversationTimeoutMs"`
	MaxConversationsPerSweep int64            `json:"maxConversationsPerSweep"`
	MaxRounds                int64            `json:"maxRounds"`
	MaxStaleRounds           int64            `json:"maxStaleRounds"`
	ListScrollStepPX         int64            `json:"listScrollStepPx"`
	Shuffle                  bool             `json:"shuffle"`
	RestoreSelection         bool             `json:"restoreSelection"`
	DryRun                   bool             `json:"dryRun"`
}

func scrollerConfigFromEnv() scrollerConfig {
	return scrollerConfig{
		Version:                  captureShimVersion,
		Selectors:                defaultSelectors,
		FilterConversations:      os.Getenv("SCROLL_NO_FILTER") != "1",
		IntervalMS:               envInt("SCROLL_INTERVAL_MS", 120_000),
		JitterMS:                 envInt("SCROLL_JITTER_MS", 15_000),
		StartDelayMS:             envInt("SCROLL_START_DELAY_MS", 3_000),
		ScrollSteps:              envInt("SCROLL_STEPS", 4),
		ScrollDelayMS:            envInt("SCROLL_DELAY_MS", 700),
		SettleAfterOpenMS:        envInt("SCROLL_SETTLE_MS", 900),
		PerConversationTimeoutMS: envInt("SCROLL_TIMEOUT_MS", 12_000),
		MaxConversationsPerSweep: envInt("SCROLL_MAX", 0),
		MaxRounds:                envInt("SCROLL_MAX_ROUNDS", 200),
		MaxStaleRounds:           envInt("SCROLL_MAX_STALE_ROUNDS", 3),
		ListScrollStepPX:         envInt("SCROLL_LIST_STEP_PX", 600),
		Shuffle:                  os.Getenv("SCROLL_SHUFFLE") == "1",
		RestoreSelection:         os.Getenv("SCROLL_NO_RESTORE") != "1",
		DryRun:                   os.Getenv("SCROLL_DRY") == "1",
	}
}

func envInt(name string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// ---------------------------------------------------------------------------
// Supervisor
// ---------------------------------------------------------------------------

// runCapture owns the durable state — the output file and both dedupe sets —
// and re-acquires a session when one dies, exactly as the Node supervisor
// relaunched Chromium. Keeping that state out here is what makes a relaunch
// idempotent: the sets never reload, so a restart cannot re-append the backlog.
func runCapture(ctx context.Context, t *Teams, cfg captureConfig) error {
	w, err := openCaptureWriter(cfg)
	if err != nil {
		return err
	}
	defer w.Close()

	captureLog("messages      %s", cfg.messagesPath)
	captureLog("conversations %s", cfg.convPath)
	if n := len(w.seenIDs); n > 0 {
		captureLog("dedupe        %s already captured", format.Count(n, "message", "messages"))
	}
	if n := len(w.conv); n > 0 {
		captureLog("directory     %s known", format.Count(n, "conversation", "conversations"))
	}

	maxRestarts := envInt("BROWSER_MAX_RESTARTS", 5)
	restartDelay := time.Duration(envInt("BROWSER_RESTART_DELAY_MS", 5*60_000)) * time.Millisecond

	var failures int64
	for {
		startedAt := time.Now()
		err := captureSession(ctx, t, cfg, w)
		if ctx.Err() != nil {
			captureLog("stopped after %s", format.Count(len(w.seenIDs), "message", "messages"))
			return nil
		}
		if err == nil {
			return nil // --once
		}
		// Login is `sessh auth login`'s job, and a profile held by
		// a process that will not share it will not free up by waiting.
		if session.IsAuth(err) || isBusy(err) {
			return err
		}
		// A session that ran healthily for a long while and then died is a fresh
		// incident, not part of a crash loop: restore the budget.
		if time.Since(startedAt) >= restartDelay {
			failures = 0
		}
		failures++
		if failures > maxRestarts {
			return fmt.Errorf("browser session failed %d time(s) in a row, giving up: %w\n"+
				"Tune with BROWSER_MAX_RESTARTS / BROWSER_RESTART_DELAY_MS", maxRestarts, err)
		}
		captureWarn("session ended: %v", err)
		captureWarn("restart %d/%d in %s", failures, maxRestarts, restartDelay.Round(time.Second))
		if err := sleepCtx(ctx, restartDelay); err != nil {
			return nil
		}
	}
}

func isBusy(err error) bool {
	var busy *session.ErrBusy
	return errors.As(err, &busy)
}

// captureSession is one browser lifetime: acquire, install, poll until the
// context is cancelled or the page stops answering.
func captureSession(ctx context.Context, t *Teams, cfg captureConfig, w *captureWriter) error {
	h, err := plugin.Acquire(ctx, t, session.AcquireOptions{
		// The whole point of this command's shape: hold the profile and lend it
		// out over the socket rather than monopolizing it.
		Serve:  true,
		Headed: cfg.headed,
		Notify: func(msg string) { captureLog("%s", msg) },
	})
	if err != nil {
		return err
	}
	defer h.Close()

	r := &captureRun{cfg: cfg, w: w, s: h.Session}
	if h.Routed {
		captureLog("sharing the browser already holding profile %q", h.Profile.Name)
	}

	landed, err := h.Session.Navigate(ctx, TeamsURL())
	if err != nil {
		return fmt.Errorf("opening %s: %w", TeamsURL(), err)
	}
	captureLog("open %s", format.Gray(landed))

	if cfg.once {
		if err := sleepCtx(ctx, idbSettleDelay); err != nil {
			return nil
		}
		return r.harvest(ctx)
	}

	if err := r.install(ctx); err != nil {
		return err
	}
	defer r.stopShims()

	captureLog("waiting %s for the app to settle before the first harvest", idbSettleDelay)
	if err := sleepCtx(ctx, idbSettleDelay); err != nil {
		return nil
	}
	if err := r.note(r.harvest(ctx)); err != nil {
		return err
	}

	poll := time.NewTicker(cfg.pollInterval)
	defer poll.Stop()
	idb := time.NewTicker(cfg.idbInterval)
	defer idb.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := r.note(r.poll(ctx)); err != nil {
				return err
			}
		case <-idb.C:
			if err := r.note(r.harvest(ctx)); err != nil {
				return err
			}
		}
	}
}

// captureRun is one session's worth of live state.
type captureRun struct {
	cfg captureConfig
	w   *captureWriter
	s   session.Session

	failures int    // consecutive Eval failures
	sweeps   int    // last in-page sweep counter we reported
	lastErr  string // suppresses a repeated warning line
}

// note decides whether an Eval failure is transient. A single failure is
// routine (the SPA navigated, a frame was busy); a run of them means the page
// or the browser is gone and the supervisor should re-acquire.
func (r *captureRun) note(err error) error {
	if err == nil {
		r.failures = 0
		r.lastErr = ""
		return nil
	}
	r.failures++
	if msg := err.Error(); msg != r.lastErr {
		captureWarn("%v", err)
		r.lastErr = msg
	}
	if r.failures >= maxEvalFailures {
		return fmt.Errorf("the Teams page stopped answering after %d attempts: %w", r.failures, err)
	}
	return nil
}

func (r *captureRun) install(ctx context.Context) error {
	var res struct {
		Installed bool `json:"installed"`
		Already   bool `json:"already"`
	}
	arg, err := evalArg(r.cfg.intercept)
	if err != nil {
		return err
	}
	if err := r.s.Eval(ctx, jsInstallInterceptor, arg, &res); err != nil {
		return fmt.Errorf("installing the in-page capture interceptor: %w", err)
	}
	if !res.Already {
		captureLog("live capture on (fetch, XHR and WebSocket, drained every %s)", r.cfg.pollInterval)
	}
	return r.installScroller(ctx)
}

func (r *captureRun) installScroller(ctx context.Context) error {
	if !r.cfg.scrollEnabled {
		captureLog("auto-scroller off (--no-scroll / SCROLL_DISABLE=1); the harvest sees only what Teams already cached")
		return nil
	}
	arg, err := evalArg(r.cfg.scroll)
	if err != nil {
		return err
	}
	var res struct {
		Installed bool `json:"installed"`
		DryRun    bool `json:"dryRun"`
	}
	if err := r.s.Eval(ctx, jsInstallScroller, arg, &res); err != nil {
		return fmt.Errorf("installing the in-page auto-scroller: %w", err)
	}
	r.sweeps = 0
	captureLog("auto-scroller on (every %ds, %d scroll-ups per chat, dryRun=%v) — note it marks chats read",
		r.cfg.scroll.IntervalMS/1000, r.cfg.scroll.ScrollSteps, res.DryRun)
	return nil
}

// stopShims leaves someone else's page clean. It only matters in the routed
// case, where the page outlives this process: an interceptor still buffering
// and a scroller still clicking through chats would be a surprise for whoever
// owns that browser.
func (r *captureRun) stopShims() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.s.Eval(ctx, jsStopShims, nil, nil)
}

// ---------------------------------------------------------------------------
// Live capture: drain the in-page buffer
// ---------------------------------------------------------------------------

func (r *captureRun) poll(ctx context.Context) error {
	var out struct {
		Capture struct {
			Installed bool `json:"installed"`
			Events    []struct {
				Source  string          `json:"source"`
				URL     string          `json:"url"`
				Payload json.RawMessage `json:"payload"`
			} `json:"events"`
			Dropped int `json:"dropped"`
			Seen    int `json:"seen"`
		} `json:"capture"`
		Scroll struct {
			Installed bool `json:"installed"`
			Sweeps    int  `json:"sweeps"`
			Opened    int  `json:"opened"`
			Errors    int  `json:"errors"`
			Last      *struct {
				Opened     int   `json:"opened"`
				Errors     int   `json:"errors"`
				Rounds     int   `json:"rounds"`
				DurationMS int64 `json:"durationMs"`
				DryRun     bool  `json:"dryRun"`
				Report     any   `json:"report"`
			} `json:"last"`
		} `json:"scroll"`
	}
	if err := r.s.Eval(ctx, jsPoll, nil, &out); err != nil {
		return fmt.Errorf("draining the in-page capture: %w", err)
	}

	written := 0
	for _, ev := range out.Capture.Events {
		payload, err := decodeJSONAny(ev.Payload)
		if err != nil {
			continue
		}
		source := ev.Source
		if source != "ws" {
			source = "rest"
		}
		for _, c := range extractMessages(payload, source) {
			added, err := r.w.write(c)
			if err != nil {
				return err
			}
			if added {
				written++
				r.logMessage(c.msg)
			}
		}
	}
	if written > 0 || out.Capture.Dropped > 0 {
		line := "live " + format.Count(written, "new message", "new messages")
		// Dropped payloads are not lost data, only lost *timeliness*: the next
		// harvest reads the same messages out of IndexedDB.
		if out.Capture.Dropped > 0 {
			line += fmt.Sprintf(", %d payload(s) dropped", out.Capture.Dropped)
		}
		captureLog("%s", line)
	}

	// The shims die with the document, and Teams v2 does real navigations of its
	// own; installed:false is how we notice and put them back.
	if !out.Capture.Installed {
		return r.install(ctx)
	}
	if r.cfg.scrollEnabled && !out.Scroll.Installed {
		return r.installScroller(ctx)
	}

	if out.Scroll.Sweeps > r.sweeps {
		r.sweeps = out.Scroll.Sweeps
		if last := out.Scroll.Last; last != nil {
			if last.DryRun {
				captureLog("scroll dry run #%d: %v", out.Scroll.Sweeps, last.Report)
			} else {
				captureLog("scroll sweep #%d: opened=%d errors=%d rounds=%d in %s",
					out.Scroll.Sweeps, last.Opened, last.Errors, last.Rounds,
					(time.Duration(last.DurationMS) * time.Millisecond).Round(time.Second))
			}
		}
		// A sweep just pulled older history into IndexedDB; take it now rather
		// than waiting out the harvest interval.
		return r.harvest(ctx)
	}
	return nil
}

// ---------------------------------------------------------------------------
// IndexedDB harvest
// ---------------------------------------------------------------------------

func (r *captureRun) harvest(ctx context.Context) error {
	var dump struct {
		Messages      json.RawMessage   `json:"messages"`
		Conversations []json.RawMessage `json:"conversations"`
		Teams         []json.RawMessage `json:"teams"`
		Errors        []string          `json:"errors"`
	}
	if err := r.s.Eval(ctx, jsDumpIDB, nil, &dump); err != nil {
		return fmt.Errorf("reading Teams' IndexedDB: %w", err)
	}

	root, err := decodeJSONAny(dump.Messages)
	if err != nil {
		return fmt.Errorf("decoding the IndexedDB dump: %w", err)
	}
	scanned := 0
	if arr, ok := root.([]any); ok {
		scanned = len(arr)
	}

	// Directory data is applied first so the messages in this same harvest can
	// resolve an MRI to a display name immediately.
	convChanged, convErr := r.w.persistConversations(dump.Conversations, dump.Teams)
	if convErr != nil {
		captureWarn("%v", convErr)
	}

	added := 0
	for _, c := range extractMessages(root, "idb") {
		ok, err := r.w.write(c)
		if err != nil {
			return err
		}
		if ok {
			added++
			r.logMessage(c.msg)
		}
	}

	if resolved := r.w.resolveConversationMembers(); resolved > 0 {
		convChanged += resolved
		if err := r.w.writeConversationsFile(); err != nil {
			captureWarn("writing %s: %v", r.w.convPath, err)
		}
	}

	line := fmt.Sprintf("idb %s (scanned=%d, conversations=%d",
		format.Count(added, "new message", "new messages"), scanned, len(dump.Conversations))
	if convChanged > 0 {
		line += fmt.Sprintf(", conv-updated=%d", convChanged)
	}
	if n := len(dump.Errors); n > 0 {
		line += fmt.Sprintf(", errors=%d", n)
	}
	captureLog("%s)", line)
	for _, e := range dump.Errors {
		captureWarn("idb: %s", e)
	}
	return nil
}

func (r *captureRun) logMessage(m *Message) {
	if !r.cfg.verbose || m == nil {
		return
	}
	sender := m.Sender
	if sender == "" {
		sender = "?"
	}
	fmt.Printf("%s %s %s %s\n",
		format.Gray(time.Now().Format("15:04:05")),
		format.Gray(strings.ToUpper(m.Source)),
		sender, format.Truncate(m.Body, 80))
}

// ---------------------------------------------------------------------------
// Output: messages.jsonl (append) + conversations.jsonl (rewrite)
// ---------------------------------------------------------------------------

type captureWriter struct {
	messagesPath string
	convPath     string
	f            *os.File

	seenIDs     map[string]struct{}
	seenContent map[string]struct{}
	resolver    *identityResolver

	// Conversation state is a map, not a log: titles and member lists MUTATE
	// (rename a group chat, add or remove people), so appending would pile up
	// stale duplicates for one id. The whole file is re-serialized whenever
	// something changes, which is cheap — a user has hundreds of conversations,
	// not millions.
	conv      map[string]*conversationRecord
	convOrder []string
}

func openCaptureWriter(cfg captureConfig) (*captureWriter, error) {
	for _, dir := range []string{filepath.Dir(cfg.messagesPath), filepath.Dir(cfg.convPath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	w := &captureWriter{
		messagesPath: cfg.messagesPath,
		convPath:     cfg.convPath,
		seenIDs:      map[string]struct{}{},
		seenContent:  map[string]struct{}{},
		resolver:     newIdentityResolver(),
		conv:         map[string]*conversationRecord{},
	}
	// Pre-populate both dedupe sets from what is already on disk, or every
	// restart would re-append the entire IndexedDB backlog.
	if _, err := os.Stat(cfg.messagesPath); err == nil {
		if _, err := StreamMessages(cfg.messagesPath, func(m *Message) {
			if m.ID == "" {
				return
			}
			w.seenIDs[m.ID] = struct{}{}
			if ck := contentKey(m); ck != "" {
				w.seenContent[ck] = struct{}{}
			}
			w.resolver.observeRaw(rawObject(m.Raw))
			w.resolver.observe(m.AuthorMRI, m.Sender)
		}, nil); err != nil {
			return nil, err
		}
	}
	if err := w.loadConversations(); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(cfg.messagesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", cfg.messagesPath, err)
	}
	w.f = f
	return w, nil
}

func (w *captureWriter) Close() {
	if w.f != nil {
		_ = w.f.Close()
	}
}

// write appends one message unless we already have it, by id or by content.
func (w *captureWriter) write(c captured) (bool, error) {
	m := c.msg
	if m == nil || m.ID == "" {
		return false, nil
	}
	normalizeMessageAttribution(m, c.raw, w.resolver)
	if _, dup := w.seenIDs[m.ID]; dup {
		return false, nil
	}
	ck := contentKeyWithRaw(m, c.raw)
	if ck != "" {
		if _, dup := w.seenContent[ck]; dup {
			return false, nil
		}
	}
	line, err := marshalJSONL(m)
	if err != nil {
		return false, nil // an unencodable record is dropped, never fatal
	}
	if _, err := w.f.Write(line); err != nil {
		return false, fmt.Errorf("appending to %s: %w", w.messagesPath, err)
	}
	w.seenIDs[m.ID] = struct{}{}
	w.resolver.observe(m.AuthorMRI, m.Sender)
	if ck != "" {
		w.seenContent[ck] = struct{}{}
	}
	return true, nil
}

// conversationRecord is one line of the sidecar: what turns a conversationId
// GUID into a human-readable name. Empty fields are omitted rather than written
// as JSON null (the Node capture wrote nulls); LoadConversations reads either.
type conversationRecord struct {
	ID                 string               `json:"id"`
	Type               string               `json:"type,omitempty"`
	Kind               string               `json:"kind,omitempty"`
	Title              string               `json:"title,omitempty"`
	DisplayName        string               `json:"displayName,omitempty"`
	ShortTitle         string               `json:"shortTitle,omitempty"`
	LongTitle          string               `json:"longTitle,omitempty"`
	ChannelName        string               `json:"channelName,omitempty"`
	TeamID             string               `json:"teamId,omitempty"`
	GroupID            string               `json:"groupId,omitempty"`
	TeamName           string               `json:"teamName,omitempty"`
	Members            []conversationMember `json:"members,omitempty"`
	MembersComplete    bool                 `json:"membersComplete,omitempty"`
	LastMessageTimeUTC json.RawMessage      `json:"lastMessageTimeUtc,omitempty"`
	UpdatedAt          string               `json:"updatedAt,omitempty"`
}

type conversationMember struct {
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	MRI         string `json:"mri,omitempty"`
}

// signature is a stable digest of the *material* fields. Re-harvested records
// that did not actually change must not churn the file or bump updatedAt, so
// the bookkeeping field is excluded (value receiver: this mutates a copy).
func (c conversationRecord) signature() string {
	c.UpdatedAt = ""
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// idbConversation is the entry shape jsDumpIDB emits. Scalars go through
// FlexString because Teams sends some of them as numbers.
type idbConversation struct {
	ID                 FlexString           `json:"id"`
	Type               FlexString           `json:"type"`
	TeamID             FlexString           `json:"teamId"`
	GroupID            FlexString           `json:"groupId"`
	Members            []conversationMember `json:"members"`
	MembersComplete    bool                 `json:"membersComplete"`
	LastMessageTimeUTC json.RawMessage      `json:"lastMessageTimeUtc"`
	ThreadProperties   *struct {
		Topic            FlexString `json:"topic"`
		TopicThreadTopic FlexString `json:"topicThreadTopic"`
		ThreadType       FlexString `json:"threadType"`
	} `json:"threadProperties"`
	ChatTitle *struct {
		Short   FlexString           `json:"short"`
		Long    FlexString           `json:"long"`
		Members []conversationMember `json:"members"`
	} `json:"chatTitle"`
}

func (w *captureWriter) loadConversations() error {
	b, err := os.ReadFile(w.convPath)
	if err != nil {
		return nil // absent is the normal first-run case
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		var rec conversationRecord
		if json.Unmarshal([]byte(t), &rec) != nil || rec.ID == "" {
			continue // a corrupt line must not cost us the rest of the file
		}
		w.put(&rec)
		for _, member := range rec.Members {
			w.resolver.observe(member.MRI, member.DisplayName)
		}
	}
	return nil
}

func (w *captureWriter) put(rec *conversationRecord) {
	if _, known := w.conv[rec.ID]; !known {
		w.convOrder = append(w.convOrder, rec.ID)
	}
	w.conv[rec.ID] = rec
}

// persistConversations merges a harvested batch and rewrites the file only when
// something actually changed.
func (w *captureWriter) persistConversations(raws, rawTeams []json.RawMessage) (int, error) {
	nowISO := isoNow()
	teamNames := explicitTeamNames(rawTeams)
	changed := 0
	for _, raw := range raws {
		var in idbConversation
		if json.Unmarshal(raw, &in) != nil {
			continue // one bad entry must never abort the batch
		}
		rec := normalizeConversation(in, teamNames, nowISO)
		if rec == nil {
			continue
		}
		if prev, ok := w.conv[rec.ID]; ok {
			rec = mergeConversation(prev, rec)
			if prev.signature() == rec.signature() {
				continue
			}
		}
		for _, member := range rec.Members {
			w.resolver.observe(member.MRI, member.DisplayName)
		}
		w.put(rec)
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if err := w.writeConversationsFile(); err != nil {
		return changed, fmt.Errorf("writing %s: %w", w.convPath, err)
	}
	return changed, nil
}

type idbTeam struct {
	ID               FlexString `json:"id"`
	TeamID           FlexString `json:"teamId"`
	ThreadID         FlexString `json:"threadId"`
	GeneralChannelID FlexString `json:"generalChannelId"`
	GroupID          FlexString `json:"groupId"`
	TeamName         FlexString `json:"teamName"`
	DisplayName      FlexString `json:"displayName"`
	Name             FlexString `json:"name"`
	Title            FlexString `json:"title"`
}

func explicitTeamNames(raws []json.RawMessage) map[string]string {
	out := map[string]string{}
	for _, raw := range raws {
		var t idbTeam
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		name := firstNonEmpty(strings.TrimSpace(t.TeamName.String()), strings.TrimSpace(t.DisplayName.String()),
			strings.TrimSpace(t.Name.String()), strings.TrimSpace(t.Title.String()))
		if name == "" {
			continue
		}
		for _, id := range []FlexString{t.ID, t.TeamID, t.ThreadID, t.GeneralChannelID, t.GroupID} {
			if key := strings.ToLower(strings.TrimSpace(id.String())); key != "" {
				out[key] = name
			}
		}
	}
	return out
}

func mergeConversation(prev, next *conversationRecord) *conversationRecord {
	if prev == nil {
		return next
	}
	if next.Type == "" {
		next.Type = prev.Type
	}
	if next.Kind == "" {
		next.Kind = prev.Kind
	}
	if next.Title == "" {
		next.Title = prev.Title
	}
	if next.DisplayName == "" {
		next.DisplayName = prev.DisplayName
	}
	if next.ShortTitle == "" {
		next.ShortTitle = prev.ShortTitle
	}
	if next.LongTitle == "" {
		next.LongTitle = prev.LongTitle
	}
	if next.ChannelName == "" {
		next.ChannelName = prev.ChannelName
	}
	if next.TeamID == "" {
		next.TeamID = prev.TeamID
	}
	if next.GroupID == "" {
		next.GroupID = prev.GroupID
	}
	if next.TeamName == "" {
		next.TeamName = prev.TeamName
	}
	if len(next.LastMessageTimeUTC) == 0 {
		next.LastMessageTimeUTC = prev.LastMessageTimeUTC
	}
	if next.MembersComplete {
		next.Members = normalizeConversationMembers(next.Members)
	} else {
		next.Members = mergeConversationMembers(prev.Members, next.Members)
		next.MembersComplete = prev.MembersComplete
	}
	return next
}

func normalizeConversationMembers(members []conversationMember) []conversationMember {
	return mergeConversationMembers(nil, members)
}

func mergeConversationMembers(groups ...[]conversationMember) []conversationMember {
	var out []conversationMember
	byMRI := map[string]int{}
	byName := map[string]int{}
	for _, members := range groups {
		for _, member := range members {
			member.MRI = canonicalAuthorMRI(member.MRI)
			member.DisplayName = cleanDisplayName(member.DisplayName)
			member.Email = strings.TrimSpace(member.Email)
			if member.MRI == "" && member.DisplayName == "" && member.Email == "" {
				continue
			}
			key := strings.ToLower(member.MRI)
			idx, ok := byMRI[key]
			if key == "" {
				idx, ok = byName[strings.ToLower(member.DisplayName)]
			}
			if !ok {
				idx = len(out)
				out = append(out, member)
				if key != "" {
					byMRI[key] = idx
				} else {
					byName[strings.ToLower(member.DisplayName)] = idx
				}
				continue
			}
			if member.DisplayName != "" {
				out[idx].DisplayName = member.DisplayName
			}
			if member.Email != "" {
				out[idx].Email = member.Email
			}
		}
	}
	return out
}

func (w *captureWriter) resolveConversationMembers() int {
	changed := 0
	for _, rec := range w.conv {
		if rec == nil {
			continue
		}
		for i := range rec.Members {
			if rec.Members[i].DisplayName != "" || rec.Members[i].MRI == "" {
				continue
			}
			if name := w.resolver.resolve(rec.Members[i].MRI); name != "" {
				rec.Members[i].DisplayName = name
				changed++
			}
		}
	}
	return changed
}

// writeConversationsFile re-serializes the whole map through a temp sibling and
// a rename, so a crash mid-write can never leave a half-written sidecar.
func (w *captureWriter) writeConversationsFile() error {
	var buf bytes.Buffer
	for _, id := range w.convOrder {
		rec := w.conv[id]
		if rec == nil {
			continue
		}
		line, err := marshalJSONL(rec)
		if err != nil {
			continue
		}
		buf.Write(line)
	}
	tmp := w.convPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.convPath)
}

func normalizeConversation(in idbConversation, teamNames map[string]string, nowISO string) *conversationRecord {
	id := strings.TrimSpace(in.ID.String())
	if id == "" {
		return nil // the dedupe key; a record without one is useless
	}
	clean := func(v FlexString) string { return strings.TrimSpace(v.String()) }

	var shortTitle, longTitle string
	var members []conversationMember
	if in.ChatTitle != nil {
		shortTitle = clean(in.ChatTitle.Short)
		longTitle = clean(in.ChatTitle.Long)
		members = in.ChatTitle.Members
	}
	if len(in.Members) > 0 {
		members = mergeConversationMembers(members, in.Members)
	} else {
		members = normalizeConversationMembers(members)
	}
	var topic, threadType string
	if in.ThreadProperties != nil {
		// Channel names do not live in chatTitle. Teams keeps them under
		// threadProperties.topic (or topicThreadTopic), and missing that is why
		// channel names once vanished from the sidecar.
		topic = firstNonEmpty(clean(in.ThreadProperties.TopicThreadTopic), clean(in.ThreadProperties.Topic))
		threadType = clean(in.ThreadProperties.ThreadType)
	}

	typ := clean(in.Type)
	kind := conversationKind(typ, id, threadType, topic)
	rec := &conversationRecord{
		ID:              id,
		Type:            typ,
		Kind:            kind,
		Title:           firstNonEmpty(topic, longTitle, shortTitle),
		DisplayName:     firstNonEmpty(topic, shortTitle, longTitle),
		ShortTitle:      shortTitle,
		LongTitle:       longTitle,
		Members:         members,
		MembersComplete: in.MembersComplete,
		UpdatedAt:       nowISO,
	}
	if kind == "channel" {
		rec.ChannelName = topic
	}
	// Keep the parent id but never infer teamName from the Space record: Teams
	// caches label it "General" (the default channel), so persisting it as a
	// team name would be a lie. An explicit team-name source can fill this in.
	if tid := clean(in.TeamID); tid != "" && tid != id {
		rec.TeamID = tid
	}
	rec.GroupID = clean(in.GroupID)
	if kind == "channel" {
		for _, key := range []string{rec.TeamID, rec.GroupID, rec.ID} {
			if name := teamNames[strings.ToLower(key)]; name != "" {
				rec.TeamName = name
				break
			}
		}
	}
	if len(in.LastMessageTimeUTC) > 0 && string(in.LastMessageTimeUTC) != "null" {
		rec.LastMessageTimeUTC = in.LastMessageTimeUTC
	}
	return rec
}

func conversationKind(typ, id, threadType, topic string) string {
	t := strings.ToLower(typ)
	tt := strings.ToLower(threadType)
	switch {
	case t == "topic", tt == "topic":
		return "channel"
	case t == "space":
		// A Space carrying a topic is the team's default channel (usually
		// "General"), not a trustworthy team record.
		if topic != "" {
			return "channel"
		}
		return "team"
	case t == "meeting", strings.HasPrefix(id, "19:meeting_"):
		return "meeting"
	case t == "chat":
		return "chat"
	case systemThreadRe.MatchString(id),
		t == "thread", t == "streamofnotes", t == "engagecommunity":
		return "system"
	case t != "":
		return t
	}
	return "unknown"
}

var systemThreadRe = regexp.MustCompile(`(?i)^(48:|streamofnotes|engagecommunity|thread$)`)

// ---------------------------------------------------------------------------
// Message extraction (very defensive — Teams payloads vary wildly)
// ---------------------------------------------------------------------------

// captured pairs a normalized message with the payload it came from, so the
// content key can read author fields out of the raw object without re-parsing
// what we just decoded.
type captured struct {
	msg *Message
	raw map[string]any
}

// looksLikeMessage is the heuristic for "this object is a chat message": the
// union of field names seen across REST, WebSocket and IndexedDB payloads.
// Presence is what counts, not the value — Teams sends plenty of nulls.
func looksLikeMessage(o map[string]any) bool {
	has := func(k string) bool { _, ok := o[k]; return ok }
	if has("messageType") || has("MessageType") {
		return true
	}
	if (has("content") || has("Content")) &&
		(has("composetime") || has("ComposeTime") ||
			has("originalarrivaltime") || has("OriginalArrivalTime") ||
			has("clientarrivaltime") || has("ClientArrivalTime")) {
		return true
	}
	// Newer graph-style payloads.
	if has("body") && (has("from") || has("From")) &&
		(has("createdDateTime") || has("lastModifiedDateTime")) {
		return true
	}
	return false
}

// pickField returns the first present, non-null, non-empty-string value.
func pickField(o map[string]any, names ...string) any {
	for _, n := range names {
		v, ok := o[n]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			continue
		}
		return v
	}
	return nil
}

// jsonString renders a decoded JSON value the way JavaScript's String() would,
// with objects and arrays falling back to their JSON text.
func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := marshalJSON(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]+>`)
	htmlEntity = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">")
)

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = htmlEntity.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func normalizeMessage(raw map[string]any, source string) *Message {
	id := strings.TrimSpace(jsonString(pickField(raw,
		"id", "Id", "messageId", "MessageId", "clientMessageId", "ClientMessageId", "clientmessageid")))
	// The Node version stringified a missing id to the literal "undefined" and
	// wrote one such record per run. A message nothing can address is useless to
	// every query downstream, so we drop it.
	if id == "" {
		return nil
	}

	contentType := jsonString(pickField(raw, "contenttype", "ContentType", "contentType"))
	var body string
	switch b := pickField(raw, "body", "Body").(type) {
	case map[string]any:
		if ct := jsonString(pickField(b, "contentType", "ContentType")); ct != "" {
			contentType = ct
		}
		body = stripHTML(jsonString(pickField(b, "content", "Content")))
	case string:
		if c, ok := pickField(raw, "content", "Content").(string); ok {
			body = stripHTML(c)
		} else {
			body = stripHTML(b)
		}
	default:
		if c, ok := pickField(raw, "content", "Content").(string); ok {
			body = stripHTML(c)
		}
	}

	m := &Message{
		ID:             id,
		ConversationID: "",
		Sender:         "",
		Body:           body,
		ContentType:    contentType,
		MessageType:    FlexString(jsonString(pickField(raw, "messageType", "MessageType", "type", "Type"))),
		ComposeTime: FlexString(jsonString(pickField(raw,
			"composetime", "ComposeTime", "createdDateTime", "CreatedDateTime", "originalArrivalTime", "OriginalArrivalTime"))),
		CapturedAt: isoNow(),
		Source:     source,
	}
	// Not encoding/json's default: it escapes <, > and & as \u00xx, and `raw` is
	// meant to be the payload Teams sent, byte-comparable with what the Node
	// capture wrote.
	if b, err := marshalJSON(raw); err == nil {
		m.Raw = b
	}
	normalizeMessageAttribution(m, raw, nil)
	return m
}

// extractMessages walks a decoded payload and pulls out every message-shaped
// object, bounded in depth and count because Teams payloads can be enormous and
// self-referential in shape.
func extractMessages(root any, source string) []captured {
	var acc []captured
	extractInto(root, source, &acc, 0)
	return acc
}

func extractInto(root any, source string, acc *[]captured, depth int) {
	if depth > 12 || len(*acc) > 5000 || root == nil {
		return
	}
	switch v := root.(type) {
	case []any:
		for _, item := range v {
			extractInto(item, source, acc, depth+1)
		}
	case map[string]any:
		if looksLikeMessage(v) {
			if m := normalizeMessage(v, source); m != nil {
				*acc = append(*acc, captured{msg: m, raw: v})
			}
		}
		// Descend even into an object that already looked like a message: some
		// wrappers nest further messages inside one. Keys are visited in sorted
		// order only because Go randomizes map iteration, and a payload should
		// extract identically on every run.
		for _, k := range sortedObjectKeys(v) {
			switch child := v[k].(type) {
			case map[string]any:
				extractInto(child, source, acc, depth+1)
			case []any:
				extractInto(child, source, acc, depth+1)
			}
		}
	}
}

func sortedObjectKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// decodeJSONAny decodes with UseNumber so a big message id or an epoch-ms
// timestamp survives the round trip into `raw` exactly as Teams sent it, rather
// than through float64.
func decodeJSONAny(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalJSON encodes without HTML escaping, so message bodies and raw payloads
// read on disk the way Teams sent them rather than as \u003c soup.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// marshalJSONL is one line of a .jsonl file.
func marshalJSONL(v any) ([]byte, error) {
	b, err := marshalJSON(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// evalArg normalizes a config struct into plain JSON values. Eval arguments are
// JSON round-tripped when the session is routed over the socket, so passing the
// same shape in both cases keeps the in-page contract identical.
func evalArg(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func captureLog(f string, a ...any) {
	fmt.Printf("%s %s\n", format.Gray(time.Now().Format("15:04:05")), fmt.Sprintf(f, a...))
}

func captureWarn(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", format.Gray(time.Now().Format("15:04:05")),
		format.Yellow(fmt.Sprintf(f, a...)))
}
