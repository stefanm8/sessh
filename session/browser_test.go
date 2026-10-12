package session

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type requestContextStub struct {
	playwright.APIRequestContext
	response playwright.APIResponse
	err      error
	opts     playwright.APIRequestContextFetchOptions
	calls    int
	wait     <-chan struct{}
	started  chan<- struct{}
}

func (s *requestContextStub) Fetch(_ any, opts ...playwright.APIRequestContextFetchOptions) (playwright.APIResponse, error) {
	s.calls++
	s.opts = opts[0]
	if s.started != nil {
		close(s.started)
	}
	if s.wait != nil {
		<-s.wait
	}
	return s.response, s.err
}

type browserContextStub struct {
	playwright.BrowserContext
	request playwright.APIRequestContext
}

func (s browserContextStub) Request() playwright.APIRequestContext { return s.request }

type responseStub struct {
	playwright.APIResponse
	disposed bool
	status   int
}

func (r *responseStub) Dispose() error             { r.disposed = true; return nil }
func (r *responseStub) Body() ([]byte, error)      { return []byte(`{"ok":true}`), nil }
func (r *responseStub) Status() int                { return r.status }
func (r *responseStub) URL() string                { return "https://tenant.example/api" }
func (r *responseStub) Headers() map[string]string { return map[string]string{} }

func TestBrowserTransportFailureIsNotAuthentication(t *testing.T) {
	failure := errors.New("socket reset after request")
	fetch := &requestContextStub{err: failure}
	s := &browserSession{ctxt: browserContextStub{request: fetch}, profile: Profile{Origin: "https://tenant.example"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := Request(ctx, http.MethodPost, "/api", []byte(`{}`))
	_, err := s.Do(ctx, req)
	if IsAuth(err) || !errors.Is(err, failure) || fetch.calls != 1 {
		t.Fatalf("error = %v, calls = %d; want an ordinary transport error without a retry", err, fetch.calls)
	}
	if fetch.opts.MaxRetries == nil || *fetch.opts.MaxRetries != 0 || fetch.opts.Timeout == nil || *fetch.opts.Timeout > 1000 || *fetch.opts.Timeout <= 0 {
		t.Fatalf("request ignores retry/deadline constraints: %+v", fetch.opts)
	}
}

func TestBrowserReleasesResponseMemory(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		raw := &responseStub{status: status}
		fetch := &requestContextStub{response: raw}
		s := &browserSession{ctxt: browserContextStub{request: fetch}, profile: Profile{Origin: "https://tenant.example"}}
		req, _ := Request(context.Background(), http.MethodGet, "/api", nil)
		res, err := s.Do(context.Background(), req)
		if status == http.StatusUnauthorized {
			if !IsAuth(err) {
				t.Fatalf("401 error = %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if string(body) != `{"ok":true}` {
				t.Fatalf("response body = %q", body)
			}
		}
		if !raw.disposed {
			t.Fatalf("response with status %d was retained", status)
		}
	}
}

func TestBrowserCancelledRequestDoesNotSend(t *testing.T) {
	fetch := &requestContextStub{}
	s := &browserSession{ctxt: browserContextStub{request: fetch}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := Request(ctx, http.MethodPost, "/api", nil)
	_, err := s.Do(context.Background(), req)
	if !errors.Is(err, context.Canceled) || fetch.calls != 0 {
		t.Fatalf("error = %v, calls = %d; want cancellation before sending", err, fetch.calls)
	}
}

func TestBrowserCancellationDuringRequest(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	fetch := &requestContextStub{wait: release, started: started, err: errors.New("interrupted")}
	s := &browserSession{ctxt: browserContextStub{request: fetch}, profile: Profile{Origin: "https://tenant.example"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(release)
	go func() { <-started; cancel() }()
	req, _ := Request(ctx, http.MethodPost, "/api", nil)
	done := make(chan error, 1)
	go func() { _, err := s.Do(ctx, req); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request = %v; want active cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request still waits for the network")
	}
}

type navigationStub struct {
	playwright.Page
	started chan struct{}
	release chan struct{}
	landed  string
	err     error
}

func (p *navigationStub) Goto(string, ...playwright.PageGotoOptions) (playwright.Response, error) {
	if p.started != nil {
		close(p.started)
	}
	if p.release != nil {
		<-p.release
	}
	return nil, p.err
}

func (p *navigationStub) URL() string {
	if p.landed != "" {
		return p.landed
	}
	return "https://tenant.example/jira/"
}

func TestBrowserCancellationDuringNavigation(t *testing.T) {
	page := &navigationStub{started: make(chan struct{}), release: make(chan struct{})}
	defer close(page.release)
	s := &browserSession{page: page, profile: Profile{Origin: "https://tenant.example"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-page.started; cancel() }()
	done := make(chan error, 1)
	go func() { _, err := s.Navigate(ctx, "/jira/"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Navigate = %v; wanted active cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled navigation still waits for the page")
	}
}

func TestBrowserNavigationErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		landed  string
		failure string
		wantErr bool
	}{
		{"DNS failure", "about:blank", "net::ERR_NAME_NOT_RESOLVED", true},
		{"timeout", "https://tenant.example/jira/", "Timeout 15000ms exceeded", true},
		{"timeout at old login page", "https://id.atlassian.com/login", "Timeout 15000ms exceeded", true},
		{"SSO interrupted on app page", "https://tenant.example/jira/", "net::ERR_ABORTED", false},
		{"SSO redirect interrupted navigation", "https://id.atlassian.com/login", "net::ERR_ABORTED", false},
		{"SSO superseded navigation", "https://login.microsoftonline.com/tenant", `Navigation to "https://tenant.example/jira/" is interrupted by another navigation to "https://login.microsoftonline.com/tenant"`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			failure := errors.New(tt.failure)
			page := &navigationStub{landed: tt.landed, err: failure}
			s := &browserSession{page: page, profile: Profile{Origin: "https://tenant.example"}}
			landed, err := s.Navigate(context.Background(), "/jira/")
			if landed != tt.landed || (tt.wantErr && !errors.Is(err, failure)) || (!tt.wantErr && err != nil) || IsAuth(err) {
				t.Fatalf("Navigate = %q, %v; want landed %q, preserve error %v", landed, err, tt.landed, tt.wantErr)
			}
		})
	}
}

type cookieContextStub struct {
	playwright.BrowserContext
	started chan struct{}
	release chan struct{}
	cookies []playwright.Cookie
	err     error
	calls   int
}

func (s *cookieContextStub) Cookies(...string) ([]playwright.Cookie, error) {
	s.calls++
	if s.started != nil {
		close(s.started)
	}
	if s.release != nil {
		<-s.release
	}
	return s.cookies, s.err
}

func TestBrowserCookiesCancellation(t *testing.T) {
	t.Run("before cookie request", func(t *testing.T) {
		cookies := &cookieContextStub{}
		s := &browserSession{ctxt: cookies}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.Cookies(ctx, "https://bitbucket.org")
		if !errors.Is(err, context.Canceled) || cookies.calls != 0 {
			t.Fatalf("Cookies = %v, calls = %d; wanted cancellation before browser work", err, cookies.calls)
		}
	})
	t.Run("during cookie request", func(t *testing.T) {
		cookies := &cookieContextStub{started: make(chan struct{}), release: make(chan struct{}), cookies: []playwright.Cookie{{Name: "session", Value: "late"}}}
		defer close(cookies.release)
		s := &browserSession{ctxt: cookies}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-cookies.started; cancel() }()
		done := make(chan error, 1)
		go func() {
			_, err := s.Cookies(ctx, "https://bitbucket.org")
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Cookies = %v; wanted active cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled Cookies still waits for the browser")
		}
	})
}

func TestBrowserCookiesKeepTransportFailure(t *testing.T) {
	failure := errors.New("browser connection closed")
	s := &browserSession{ctxt: &cookieContextStub{err: failure}}
	if _, err := s.Cookies(context.Background(), "https://bitbucket.org"); !errors.Is(err, failure) || IsAuth(err) {
		t.Fatalf("Cookies = %v; wanted original transport error", err)
	}
}

func TestCachedIdentityDoesNotAccessBrowserOrRPC(t *testing.T) {
	id := Identity{AccountID: "already-verified", DisplayName: "Test User"}
	for _, tt := range []struct {
		name    string
		session interface {
			RememberIdentity(Identity)
			CachedIdentity() Identity
		}
	}{
		{"browser", &browserSession{}},
		{"RPC", &rpcClient{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.session.CachedIdentity(); !got.Empty() {
				t.Fatalf("initial cached identity = %+v", got)
			}
			tt.session.RememberIdentity(id)
			if got := tt.session.CachedIdentity(); got != id {
				t.Fatalf("cached identity = %+v; wanted %+v", got, id)
			}
		})
	}
}
