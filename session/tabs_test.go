package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type pooledPageStub struct {
	playwright.Page
	mu         sync.Mutex
	url        string
	closed     bool
	closeCalls int
	gotos      int
}

func (p *pooledPageStub) Goto(url string, _ ...playwright.PageGotoOptions) (playwright.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.url = url
	p.gotos++
	return nil, nil
}

func (p *pooledPageStub) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}

func (p *pooledPageStub) IsClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *pooledPageStub) Close(...playwright.PageCloseOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.closeCalls++
	return nil
}

func (p *pooledPageStub) Evaluate(string, ...any) (any, error) { return p.URL(), nil }

type pooledContextStub struct {
	playwright.BrowserContext
	mu      sync.Mutex
	pages   []*pooledPageStub
	started chan struct{}
	resume  chan struct{}
}

func (c *pooledContextStub) NewPage() (playwright.Page, error) {
	if c.started != nil {
		close(c.started)
		<-c.resume
	}
	p := &pooledPageStub{url: "about:blank"}
	c.mu.Lock()
	c.pages = append(c.pages, p)
	c.mu.Unlock()
	return p, nil
}

type poolListenerStub struct{ net.Listener }

func (poolListenerStub) Close() error { return nil }

func pooledServer(t *testing.T) (*Server, *pooledContextStub) {
	t.Helper()
	c := &pooledContextStub{}
	s := &Server{
		sess:    &browserSession{ctxt: c, profile: Profile{Name: "shared", Origin: "https://holder.example"}},
		ln:      poolListenerStub{},
		closing: make(chan struct{}),
	}
	t.Cleanup(s.Close)
	return s, c
}

func TestWarmPageLeaseReusesCallerOrigin(t *testing.T) {
	s, c := pooledServer(t)
	first, release, err := s.leasePage("https://CALLER.example:443/app/?q=one")
	if err != nil {
		t.Fatal(err)
	}
	var location string
	if err := first.Eval(context.Background(), "location.href", nil, &location); err != nil {
		t.Fatal(err)
	}
	first.RememberIdentity(Identity{AccountID: "previous-command"})
	release()
	second, releaseSecond, err := s.leasePage("https://caller.example/other/")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if second != first || len(c.pages) != 1 || !second.CachedIdentity().Empty() {
		t.Fatalf("warm lease = %p, first = %p, pages = %d, identity retained = %v", second, first, len(c.pages), !second.CachedIdentity().Empty())
	}
	if err := second.Eval(context.Background(), "location.href", nil, &location); err != nil {
		t.Fatal(err)
	}
	if c.pages[0].gotos != 1 || pageOrigin(location) != "https://caller.example" {
		t.Fatalf("warm page navigated again or used holder origin: gotos=%d, location=%s", c.pages[0].gotos, location)
	}
}

func TestWarmPageLeaseIsolationAndCap(t *testing.T) {
	s, c := pooledServer(t)
	first, releaseFirst, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	second, releaseSecond, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	if first.page == second.page {
		t.Fatal("concurrent clients share a page")
	}
	releaseFirst()
	releaseFirst() // release is idempotent
	releaseSecond()
	if len(s.idleTabs) != 1 || !second.page.IsClosed() || first.page.IsClosed() {
		t.Fatal("pool did not retain exactly one page for the origin")
	}
	for i := range maxIdleTabs {
		_, release, err := s.leasePage(fmt.Sprintf("https://caller%d.example", i))
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if len(s.idleTabs) != maxIdleTabs || !first.page.IsClosed() {
		t.Fatalf("idle cap did not evict oldest page: idle=%d, oldest closed=%v", len(s.idleTabs), first.page.IsClosed())
	}
	s.Close()
	for _, p := range c.pages {
		if !p.IsClosed() || p.closeCalls != 1 {
			t.Fatalf("shutdown left a page open or closed twice: closed=%v calls=%d", p.IsClosed(), p.closeCalls)
		}
	}
}

func TestWarmPageOffOriginAndClosedRecovery(t *testing.T) {
	s, c := pooledServer(t)
	first, release, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Navigate(context.Background(), "https://login.example/signin"); err != nil {
		t.Fatal(err)
	}
	release()
	if first.primed {
		t.Fatal("off-origin page was left primed")
	}
	second, releaseSecond, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	var location string
	if err := second.Eval(context.Background(), "location.href", nil, &location); err != nil {
		t.Fatal(err)
	}
	if location != "https://caller.example/" || c.pages[0].gotos != 2 {
		t.Fatalf("off-origin page did not return to caller: %s, gotos=%d", location, c.pages[0].gotos)
	}
	releaseSecond()
	_ = second.page.Close() // a tab may close itself while idle
	third, releaseThird, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseThird()
	if third == second || len(c.pages) != 2 {
		t.Fatal("closed tab was reused")
	}
}

func TestWarmPageShutdownDuringCreationAndLease(t *testing.T) {
	t.Run("creation", func(t *testing.T) {
		s, c := pooledServer(t)
		c.started, c.resume = make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() { _, _, err := s.leasePage("https://caller.example"); done <- err }()
		<-c.started
		s.Close()
		close(c.resume)
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("lease during shutdown = %v", err)
		}
		if len(c.pages) != 1 || !c.pages[0].IsClosed() || len(s.idleTabs) != 0 {
			t.Fatal("shutdown leaked a newly created page")
		}
	})
	t.Run("leased", func(t *testing.T) {
		s, _ := pooledServer(t)
		client, release, err := s.leasePage("https://caller.example")
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		release()
		if !client.page.IsClosed() || len(s.idleTabs) != 0 {
			t.Fatal("returning a lease after shutdown leaked its page")
		}
		if _, _, err := s.leasePage("https://caller.example"); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("lease after shutdown = %v", err)
		}
	})
}

func TestWarmPageDoesNotReuseUnsettledNavigation(t *testing.T) {
	s, _ := pooledServer(t)
	page := &blockedNavigationPageStub{blockedEvalPageStub: blockedEvalPageStub{started: make(chan struct{}), release: make(chan struct{})}}
	s.sess.ctxt = singlePageContextStub{page: page}
	client, release, err := s.leasePage("https://caller.example")
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation may return while Playwright is still navigating. Returning
	// that lease must close it, so the old navigation cannot affect a new client.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-page.started; cancel() }()
	if _, err := client.Navigate(ctx, "/slow"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Navigate = %v; wanted cancellation", err)
	}
	release()
	if !client.page.IsClosed() || len(s.idleTabs) != 0 {
		t.Fatal("unsettled navigation entered the warm pool")
	}
}

func TestRPCWarmLeaseOriginsAndIdle(t *testing.T) {
	for _, origin := range []string{"https://caller.example", ""} {
		t.Run(origin, func(t *testing.T) {
			s, c := pooledServer(t)
			s.lastActivity = time.Now().Add(-time.Hour)
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() { s.handle(server); close(done) }()
			defer func() { _ = client.Close(); <-done }()
			dec, enc := json.NewDecoder(client), json.NewEncoder(client)
			call := func(req rpcRequest) rpcResponse {
				t.Helper()
				if err := enc.Encode(req); err != nil {
					t.Fatal(err)
				}
				var res rpcResponse
				if err := dec.Decode(&res); err != nil {
					t.Fatal(err)
				}
				if res.Error != "" {
					t.Fatal(res.Error)
				}
				return res
			}
			call(rpcRequest{Method: "ping"})
			if len(c.pages) != 0 || s.IdleFor() < 59*time.Minute {
				t.Fatal("health probe created a tab or acquired activity")
			}
			res := call(rpcRequest{Method: "eval", Origin: origin, Params: json.RawMessage(`{"js":"location.href"}`)})
			want := origin
			if want == "" {
				want = "https://holder.example"
			}
			var location string
			if err := json.Unmarshal(res.Result, &location); err != nil || location != want+"/" {
				t.Fatalf("Eval location = %s, err = %v; want %s", location, err, want)
			}
			if s.IdleFor() != 0 {
				t.Fatal("connected client counted as idle between requests")
			}
		})
	}
}

func TestRememberIdentityIsPerSessionAndSkipsEmpty(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		t.Run(fmt.Sprint("rpc=", rpc), func(t *testing.T) {
			calls := 0
			profile := Profile{Whoami: func(context.Context, Session) (Identity, error) {
				calls++
				if calls == 1 {
					return Identity{}, nil
				}
				if calls == 2 {
					return Identity{}, errors.New("try again")
				}
				return Identity{AccountID: "signed-in"}, nil
			}}
			var s Session = &browserSession{profile: profile}
			if rpc {
				s = &rpcClient{profile: profile}
			}
			_, _ = s.Whoami(context.Background())
			_, _ = s.Whoami(context.Background())
			for range 2 {
				id, err := s.Whoami(context.Background())
				if err != nil || id.AccountID != "signed-in" {
					t.Fatalf("Whoami = %+v, %v", id, err)
				}
			}
			if calls != 3 {
				t.Fatalf("identity calls = %d; wanted no cached anonymous identity or error", calls)
			}
			RememberIdentity(Observe(s), Identity{AccountID: "verified"})
			RememberIdentity(s, Identity{})
			id, err := s.Whoami(context.Background())
			if err != nil || id.AccountID != "verified" || calls != 3 {
				t.Fatalf("remembered identity = %+v, %v, calls=%d", id, err, calls)
			}
		})
	}
}

type blockedEvalPageStub struct {
	pooledPageStub
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockedEvalPageStub) Evaluate(string, ...any) (any, error) {
	close(p.started)
	<-p.release
	return nil, errors.New("page closed")
}

func (p *blockedEvalPageStub) Close(...playwright.PageCloseOptions) error {
	p.once.Do(func() { close(p.release) })
	return p.pooledPageStub.Close()
}

type singlePageContextStub struct {
	playwright.BrowserContext
	page playwright.Page
}

type blockedNavigationPageStub struct{ blockedEvalPageStub }

func (p *blockedNavigationPageStub) Goto(string, ...playwright.PageGotoOptions) (playwright.Response, error) {
	close(p.started)
	<-p.release
	return nil, errors.New("page closed")
}

func (c singlePageContextStub) NewPage() (playwright.Page, error) { return c.page, nil }

func TestRPCEvalDisconnectReleasesPendingPage(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint("shutdown=", shutdown), func(t *testing.T) {
			s, _ := pooledServer(t)
			page := &blockedEvalPageStub{started: make(chan struct{}), release: make(chan struct{})}
			s.sess.ctxt = singlePageContextStub{page: page}
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() { s.handle(server); close(done) }()
			if err := json.NewEncoder(client).Encode(rpcRequest{Method: "eval", Origin: "https://caller.example", Params: json.RawMessage(`{"js":"new Promise(() => {})"}`)}); err != nil {
				t.Fatal(err)
			}
			<-page.started
			if shutdown {
				s.Close()
			} else {
				_ = client.Close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = page.Close()
				t.Fatal("disconnected client left an Eval running")
			}
			if !page.IsClosed() || len(s.idleTabs) != 0 || s.activeClients != 0 {
				t.Fatalf("pending Eval page was retained: closed=%v, idle=%d, clients=%d", page.IsClosed(), len(s.idleTabs), s.activeClients)
			}
		})
	}
}

type httpPoolContextStub struct {
	pooledContextStub
	request playwright.APIRequestContext
}

func (c *httpPoolContextStub) Request() playwright.APIRequestContext { return c.request }

func TestRPCHTTPOnlyClientDoesNotLeasePage(t *testing.T) {
	s, _ := pooledServer(t)
	fetch := &requestContextStub{response: &responseStub{status: http.StatusOK}}
	ctxt := &httpPoolContextStub{request: fetch}
	s.sess.ctxt = ctxt
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()
	c := &rpcClient{conn: client, dec: json.NewDecoder(client), profile: Profile{Origin: "https://caller.example"}}
	defer func() { _ = c.Close(); <-done }()
	req, _ := Request(context.Background(), http.MethodGet, "/api", nil)
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(ctxt.pages) != 0 || len(s.idleTabs) != 0 {
		t.Fatal("an HTTP-only command created a page")
	}
}

func TestRPCEvalSendsCallerOrigin(t *testing.T) {
	client, server := net.Pipe()
	seen := make(chan rpcRequest, 1)
	fakeHolder(t, server, func(req rpcRequest) *rpcResponse {
		seen <- req
		return &rpcResponse{Result: json.RawMessage(`null`)}
	})
	c := &rpcClient{conn: client, dec: json.NewDecoder(client), profile: Profile{Origin: "https://caller.example"}}
	defer c.Close()
	if err := c.Eval(context.Background(), "1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if req := <-seen; req.Method != "eval" || req.Origin != c.profile.Origin {
		t.Fatalf("Eval request did not convey caller origin: %+v", req)
	}
}
