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
}

func (p *navigationStub) Goto(string, ...playwright.PageGotoOptions) (playwright.Response, error) {
	close(p.started)
	<-p.release
	return nil, nil
}

func (p *navigationStub) URL() string { return "https://tenant.example/jira/" }

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
