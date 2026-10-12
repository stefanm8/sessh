package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// browserSession is the in-process implementation: a Chromium persistent
// context plus one page.
//
// HTTP goes through Playwright's APIRequestContext, which shares the context's
// cookie jar and is not subject to CORS — the reason Bitbucket's same-origin
// `/!api` trick and Jira's in-page fetch problems both disappear here.
type browserSession struct {
	identityCache
	ctxt    playwright.BrowserContext
	page    playwright.Page
	profile Profile

	mu       sync.Mutex
	primed   bool // page has been navigated to the origin at least once
	unusable bool // a cancelled navigation may still be running; do not pool its page
}

var _ Session = (*browserSession)(nil)

func (s *browserSession) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(req.Context(), cancel)
	defer func() { stop(); cancel() }()
	requestURL := ResolveAgainst(s.profile.Origin, req.URL.String())

	headers := map[string]string{}
	for k := range req.Header {
		headers[k] = req.Header.Get(k)
	}
	if _, ok := headers["Accept"]; !ok {
		headers["Accept"] = "application/json"
	}

	opts := playwright.APIRequestContextFetchOptions{
		Method:           playwright.String(req.Method),
		Headers:          headers,
		FailOnStatusCode: playwright.Bool(false),
		Timeout:          playwright.Float(requestTimeout(ctx, req.Context())),
		MaxRetries:       playwright.Int(0),
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if len(body) > 0 {
			opts.Data = body
		}
	}

	type result struct {
		response playwright.APIResponse
		err      error
	}
	done := make(chan result)
	go func() {
		res, err := s.ctxt.Request().Fetch(requestURL, opts)
		select {
		case done <- result{res, err}:
		case <-ctx.Done():
			if res != nil {
				_ = res.Dispose()
			}
		}
	}()
	var res playwright.APIResponse
	var err error
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		res, err = r.response, r.err
	}
	if err != nil {
		// A transport failure is not proof of a lapsed session. In particular,
		// a write may have succeeded before its response was lost.
		return nil, fmt.Errorf("request to %s failed: %w", s.profile.Origin, err)
	}
	defer res.Dispose()
	body, err := res.Body()
	if err != nil {
		return nil, err
	}
	status := res.Status()
	finalURL := res.URL()

	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, &AuthError{Profile: s.profile.Name, Status: status, URL: requestURL, Detail: fmt.Sprintf(
			"not authenticated (HTTP %d) for %s. Run: sessh auth login --profile %s",
			status, s.profile.Origin, s.profile.Name)}
	}
	if IsLoginURL(finalURL) {
		return nil, &AuthError{Profile: s.profile.Name, URL: requestURL, Detail: fmt.Sprintf(
			"session expired (redirected to %s). Run: sessh auth login --profile %s",
			finalURL, s.profile.Name)}
	}

	hdr := http.Header{}
	for k, v := range res.Headers() {
		hdr.Set(k, v)
	}
	outReq := req.Clone(req.Context())
	if u, err := url.Parse(finalURL); err == nil {
		outReq.URL = u
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Header:        hdr,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       outReq,
	}, nil
}

// requestTimeout bounds Playwright's request by either caller's deadline.
// Playwright uses milliseconds and interprets zero as no timeout.
func requestTimeout(contexts ...context.Context) float64 {
	timeout := time.Minute
	for _, ctx := range contexts {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < timeout {
				timeout = remaining
			}
		}
	}
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	return float64(timeout) / float64(time.Millisecond)
}

func (s *browserSession) GetJSON(ctx context.Context, path string, out any) error {
	return getJSON(ctx, s, path, out)
}

func (s *browserSession) Eval(ctx context.Context, js string, arg, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.prime(ctx); err != nil {
		return err
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result)
	go func() {
		data, err := s.evaluate(ctx, js, arg)
		select {
		case done <- result{data, err}:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		return ctx.Err()
	case res := <-done:
		if res.err != nil {
			return res.err
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(res.data, out)
	}
}

func (s *browserSession) evaluate(ctx context.Context, js string, arg any) ([]byte, error) {
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	// A page navigation can destroy the execution context mid-evaluate (an SSO
	// redirect during login does exactly this). That is transient, so settle
	// and retry once before reporting failure.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.page.Evaluate(js, args...)
		if err == nil {
			return json.Marshal(res)
		}
		lastErr = err
		if !IsNavigationTransient(err) {
			break
		}
		_ = s.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State:   playwright.LoadStateDomcontentloaded,
			Timeout: playwright.Float(requestTimeout(ctx)),
		})
	}
	return nil, lastErr
}

// IsNavigationTransient identifies an execution context replaced by a redirect.
func IsNavigationTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "execution context was destroyed") ||
		strings.Contains(msg, "navigating and changing")
}

func (s *browserSession) Navigate(ctx context.Context, rawurl string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	url := ResolveAgainst(s.profile.Origin, rawurl)
	// Keep browser work bounded by the caller's verification budget. Most
	// navigation errors are transport failures, not evidence of signed-out
	// state. SSO can supersede navigation on either a login or an app page;
	// the verifier must inspect the resulting session in that case.
	done := make(chan error, 1)
	go func() {
		_, err := s.page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(requestTimeout(ctx)),
		})
		done <- err
	}()
	var err error
	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		return "", ctx.Err()
	case err = <-done:
	}
	s.mu.Lock()
	s.primed = true
	s.mu.Unlock()
	landed := s.page.URL()
	if err != nil {
		message := strings.ToLower(err.Error())
		interrupted := strings.Contains(message, "net::err_aborted") ||
			strings.Contains(message, "is interrupted by another navigation")
		if !interrupted {
			return landed, fmt.Errorf("navigation to %s failed: %w", url, err)
		}
	}
	return landed, nil
}

// Observe wraps a session so that Navigate reports where the page already is
// instead of moving it. Interactive login polls Verify through it: a Verify
// that navigates to mint a session would otherwise reload the page on which
// the user is typing credentials.
func Observe(s Session) Session {
	if _, ok := s.(observer); ok {
		return s
	}
	return observer{s}
}

type observer struct{ Session }

func (o observer) Navigate(ctx context.Context, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if b, ok := o.Session.(*browserSession); ok {
		return b.page.URL(), nil
	}
	var href string
	err := o.Session.Eval(ctx, `() => location.href`, nil, &href)
	return href, err
}

func (s *browserSession) Cookies(ctx context.Context, rawurl string) ([]Cookie, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	url := ResolveAgainst(s.profile.Origin, rawurl)
	type result struct {
		cookies []Cookie
		err     error
	}
	done := make(chan result, 1)
	go func() {
		cs, err := s.ctxt.Cookies(url)
		out := make([]Cookie, 0, len(cs))
		for _, c := range cs {
			out = append(out, Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path})
		}
		done <- result{out, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		return res.cookies, res.err
	}
}

func (s *browserSession) Whoami(ctx context.Context) (Identity, error) {
	if id := s.CachedIdentity(); !id.Empty() {
		return id, nil
	}

	if s.profile.Whoami == nil {
		return Identity{}, nil
	}
	id, err := s.profile.Whoami(ctx, s)
	if err != nil {
		return Identity{}, err
	}
	s.RememberIdentity(id)
	return id, nil
}

// pageOrigin compares actual browser origins, ignoring paths and spelling
// differences that do not change an HTTP origin.
func pageOrigin(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if scheme == "http" && u.Port() == "80" || scheme == "https" && u.Port() == "443" {
		host = strings.TrimSuffix(host, ":"+u.Port())
	}
	return scheme + "://" + host
}

// prime makes sure the page actually sits on the profile's origin before we
// run JavaScript in it. API reads need no navigation (cookies come off disk at
// launch), so we only pay for this when Eval is used.
func (s *browserSession) prime(ctx context.Context) error {
	s.mu.Lock()
	primed := s.primed
	s.mu.Unlock()
	if primed {
		return nil
	}
	_, err := s.Navigate(ctx, "/")
	return err
}

// fork returns a session sharing this one's browser context but with a page of
// its own. The manager hands these to socket clients so that a command routed
// through a long-running holder cannot navigate the holder's page out from
// under it — a `sessh teams capture` must keep the page it is scrolling.
func (s *browserSession) fork(origin string) (*browserSession, error) {
	page, err := s.ctxt.NewPage()
	if err != nil {
		return nil, err
	}
	return &browserSession{ctxt: s.ctxt, page: page, profile: Profile{Name: s.profile.Name, Origin: origin}}, nil
}

// StorageState exports the profile's cookies and localStorage — the seed a
// child profile inherits.
func (s *browserSession) StorageState() (json.RawMessage, error) {
	st, err := s.ctxt.StorageState()
	if err != nil {
		return nil, err
	}
	return json.Marshal(st)
}

// ---------------------------------------------------------------------------
// Launch
// ---------------------------------------------------------------------------

type launchOptions struct {
	Headed bool
}

func launch(profile Profile, opts launchOptions) (*browserSession, func(), error) {
	return launchDir(ProfileDir(profile.Name), profile, opts)
}

// launchDir opens a specific user-data directory. Everything goes through
// ProfileDir except ImportProfile, which reads an existing Chromium profile.
func launchDir(dir string, profile Profile, opts launchOptions) (*browserSession, func(), error) {
	pw, err := playwrightRun()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}

	ctxt, err := pw.Chromium.LaunchPersistentContext(dir, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(!opts.Headed),
		Viewport: &playwright.Size{Width: 1366, Height: 850},
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-default-browser-check",
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("launching browser for profile %q: %w", profile.Name, err)
	}

	// Apply a pending inheritance seed before anything touches the network.
	if err := applySeed(ctxt, profile.Name); err != nil {
		_ = ctxt.Close()
		return nil, nil, err
	}

	var page playwright.Page
	if pages := ctxt.Pages(); len(pages) > 0 {
		page = pages[0]
	} else {
		page, err = ctxt.NewPage()
		if err != nil {
			_ = ctxt.Close()
			return nil, nil, err
		}
	}

	s := &browserSession{ctxt: ctxt, page: page, profile: profile}
	closer := func() {
		// Give Chromium a moment to flush its cookie jar before the process
		// exits, so a login in this run is visible to the next one.
		done := make(chan struct{})
		go func() {
			_ = ctxt.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	}
	return s, closer, nil
}
