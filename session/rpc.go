package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Across processes, an OS lock on the profile directory decides ownership.
// The holder serves this JSON-RPC over a unix socket; everyone
// else gets a Session whose calls are forwarded to it. The interface is
// identical on both sides, so a plugin cannot tell which one it has — which is
// what makes `sessh teams capture` holding the profile for hours a non-event for
// every other command.

type rpcRequest struct {
	ID       int             `json:"id"`
	Method   string          `json:"method"`
	Origin   string          `json:"origin,omitempty"` // caller's origin; older clients use the holder's
	Params   json.RawMessage `json:"params,omitempty"`
	Deadline int64           `json:"deadline,omitempty"` // Unix milliseconds; older clients omit it
}

type rpcResponse struct {
	ID         int             `json:"id"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Auth       bool            `json:"auth,omitempty"` // error is an ErrAuth
	AuthStatus int             `json:"authStatus,omitempty"`
	AuthURL    string          `json:"authURL,omitempty"`
}

type doParams struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

type doResult struct {
	Status  int               `json:"status"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

type evalParams struct {
	JS  string          `json:"js"`
	Arg json.RawMessage `json:"arg,omitempty"`
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server exposes a live session on the profile's socket for the lifetime of
// the lock holder.
type Server struct {
	sess           *browserSession
	ln             net.Listener
	path           string
	closing        chan struct{}
	once           sync.Once
	activityMu     sync.Mutex
	lastActivity   time.Time
	activeRequests int
	activeClients  int
	tabsMu         sync.Mutex
	idleTabs       []*browserSession // oldest first, at most one per origin
	tabsClosed     bool
}

const maxIdleTabs = 8

func serve(s *browserSession, name string) (*Server, error) {
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		return nil, err
	}
	path := SocketPath(name)
	// A socket left behind by a crashed holder would otherwise block Listen.
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 300*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("socket %s is already served", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, socketError(err, path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restricting session socket permissions: %w", err)
	}
	srv := &Server{sess: s, ln: ln, path: path, closing: make(chan struct{}), lastActivity: time.Now()}
	go srv.acceptLoop()
	return srv, nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
				return
			}
		}
		go s.handle(conn)
	}
}

func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closing)
		_ = s.ln.Close()
		_ = os.Remove(s.path)
		s.tabsMu.Lock()
		s.tabsClosed = true
		idle := s.idleTabs
		s.idleTabs = nil
		s.tabsMu.Unlock()
		for _, tab := range idle {
			_ = tab.page.Close()
		}
	})
}

// leasePage gives one connection exclusive use of a page. Idle pages retain
// their DOM, but never the previous connection's identity or authorization.
func (s *Server) leasePage(origin string) (*browserSession, func(), error) {
	if origin == "" {
		origin = s.sess.profile.Origin
	}
	key := pageOrigin(origin)
	s.tabsMu.Lock()
	if s.tabsClosed {
		s.tabsMu.Unlock()
		return nil, nil, net.ErrClosed
	}
	var client *browserSession
	for i, tab := range s.idleTabs {
		if pageOrigin(tab.profile.Origin) == key {
			client = tab
			s.idleTabs = append(s.idleTabs[:i], s.idleTabs[i+1:]...)
			break
		}
	}
	s.tabsMu.Unlock()
	if client != nil && client.page.IsClosed() {
		client = nil
	}
	if client == nil {
		var err error
		client, err = s.sess.fork(origin)
		if err != nil {
			return nil, nil, err
		}
	} else {
		client.profile.Origin = origin
		client.mu.Lock()
		client.primed = key != "" && pageOrigin(client.page.URL()) == key
		client.mu.Unlock()
	}
	// Creation happens without the pool lock; shutdown may have won meanwhile.
	s.tabsMu.Lock()
	closed := s.tabsClosed
	s.tabsMu.Unlock()
	if closed {
		_ = client.page.Close()
		return nil, nil, net.ErrClosed
	}
	var once sync.Once
	return client, func() { once.Do(func() { s.releasePage(client) }) }, nil
}

func (s *Server) releasePage(client *browserSession) {
	key := pageOrigin(client.profile.Origin)
	client.mu.Lock()
	client.ident, client.hasIdnt = Identity{}, false
	usable := !client.unusable && !client.page.IsClosed()
	client.primed = usable && key != "" && pageOrigin(client.page.URL()) == key
	client.mu.Unlock()
	var discard *browserSession
	s.tabsMu.Lock()
	if s.tabsClosed || !usable || key == "" {
		discard = client
	} else {
		for _, tab := range s.idleTabs {
			if pageOrigin(tab.profile.Origin) == key {
				discard = client
				break
			}
		}
		if discard == nil {
			if len(s.idleTabs) == maxIdleTabs {
				discard = s.idleTabs[0]
				s.idleTabs = s.idleTabs[1:]
			}
			s.idleTabs = append(s.idleTabs, client)
		}
	}
	s.tabsMu.Unlock()
	if discard != nil {
		_ = discard.page.Close()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.closing:
			cancel()
			_ = conn.Close()
		case <-ctx.Done():
		}
	}()

	// Page work leases an exclusive page on first use and returns it to the
	// warm pool with the connection. HTTP-only clients never create a page.
	var (
		client        *browserSession
		release       func()
		requestOrigin string
		active        bool
	)
	defer func() {
		if release != nil {
			release()
		}
		if active {
			s.activityMu.Lock()
			s.activeClients--
			s.lastActivity = time.Now()
			s.activityMu.Unlock()
		}
	}()
	pageSession := func() (*browserSession, error) {
		origin := requestOrigin
		if origin == "" {
			origin = s.sess.profile.Origin
		}
		if client != nil && client.profile.Origin == origin {
			return client, nil
		}
		if release != nil {
			release()
			client, release = nil, nil
		}
		c, closer, err := s.leasePage(origin)
		if err != nil {
			return nil, err
		}
		client, release = c, closer
		return client, nil
	}

	w := bufio.NewWriterSize(conn, 1<<20)
	requests := make(chan rpcRequest)
	// Keep one reader waiting even while dispatch is running, so disconnecting
	// a cancelled CLI also interrupts its browser work and releases its lease.
	go func() {
		defer cancel()
		dec := json.NewDecoder(bufio.NewReaderSize(conn, 1<<20))
		for {
			var req rpcRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			select {
			case requests <- req:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var req rpcRequest
		select {
		case req = <-requests:
		case <-ctx.Done():
			return
		}
		if req.Method != "ping" && !active {
			s.activityMu.Lock()
			s.activeClients++
			s.activityMu.Unlock()
			active = true
		}
		requestOrigin = req.Origin
		res := s.dispatchContext(ctx, &req, pageSession)
		b, err := json.Marshal(res)
		if err != nil {
			return
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(req *rpcRequest, pageSession func() (*browserSession, error)) *rpcResponse {
	return s.dispatchContext(context.Background(), req, pageSession)
}

func (s *Server) dispatchContext(parent context.Context, req *rpcRequest, pageSession func() (*browserSession, error)) *rpcResponse {
	if req.Method != "ping" {
		s.activityMu.Lock()
		s.activeRequests++
		s.activityMu.Unlock()
		defer func() {
			s.activityMu.Lock()
			s.activeRequests--
			s.lastActivity = time.Now()
			s.activityMu.Unlock()
		}()
	}
	deadline := time.Now().Add(65 * time.Second)
	if req.Deadline != 0 && time.UnixMilli(req.Deadline).Before(deadline) {
		deadline = time.UnixMilli(req.Deadline)
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	out := &rpcResponse{ID: req.ID}
	fail := func(err error) *rpcResponse {
		out.Error = err.Error()
		out.Auth = IsAuth(err)
		var ae *AuthError
		if errors.As(err, &ae) {
			out.AuthStatus, out.AuthURL = ae.Status, ae.URL
		}
		return out
	}
	ok := func(v any) *rpcResponse {
		b, err := json.Marshal(v)
		if err != nil {
			return fail(err)
		}
		out.Result = b
		return out
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	switch req.Method {
	case "ping":
		return ok("pong")

	case "do":
		var p doParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		hreq, err := Request(ctx, p.Method, p.Path, p.Body)
		if err != nil {
			return fail(err)
		}
		for k, v := range p.Headers {
			hreq.Header.Set(k, v)
		}
		resp, err := s.sess.Do(ctx, hreq)
		if err != nil {
			return fail(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fail(err)
		}
		hdr := map[string]string{}
		for k := range resp.Header {
			hdr[k] = resp.Header.Get(k)
		}
		final := ""
		if resp.Request != nil && resp.Request.URL != nil {
			final = resp.Request.URL.String()
		}
		return ok(doResult{Status: resp.StatusCode, URL: final, Headers: hdr, Body: body})

	case "eval":
		var p evalParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		var arg any
		if len(p.Arg) > 0 {
			if err := json.Unmarshal(p.Arg, &arg); err != nil {
				return fail(err)
			}
		}
		ps, err := pageSession()
		if err != nil {
			return fail(err)
		}
		var res json.RawMessage
		if err := ps.Eval(ctx, p.JS, arg, &res); err != nil {
			return fail(err)
		}
		return ok(res)

	case "navigate":
		var url string
		if err := json.Unmarshal(req.Params, &url); err != nil {
			return fail(err)
		}
		ps, err := pageSession()
		if err != nil {
			return fail(err)
		}
		final, err := ps.Navigate(ctx, url)
		if err != nil {
			return fail(err)
		}
		return ok(final)

	case "cookies":
		var url string
		if err := json.Unmarshal(req.Params, &url); err != nil {
			return fail(err)
		}
		cs, err := s.sess.Cookies(ctx, url)
		if err != nil {
			return fail(err)
		}
		return ok(cs)

	case "whoami":
		id, err := s.sess.Whoami(ctx)
		if err != nil {
			return fail(err)
		}
		return ok(id)

	case "storagestate":
		st, err := s.sess.StorageState()
		if err != nil {
			return fail(err)
		}
		return ok(st)

	default:
		return fail(fmt.Errorf("unknown method %q", req.Method))
	}
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

type rpcClient struct {
	conn       net.Conn
	dec        *json.Decoder
	gate       chan struct{}
	gateOnce   sync.Once
	nextID     int
	profile    Profile
	identityMu sync.Mutex
	ident      Identity
	hasIdnt    bool
}

var _ Session = (*rpcClient)(nil)

func dialProfile(name string) (*rpcClient, error) {
	return dialProfileContext(context.Background(), name)
}

func dialProfileContext(ctx context.Context, name string) (*rpcClient, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(name))
	if err != nil {
		return nil, err
	}
	c := &rpcClient{conn: conn, dec: json.NewDecoder(bufio.NewReaderSize(conn, 1<<20))}
	if err := c.callContext(ctx, "ping", nil, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *rpcClient) Close() error { return c.conn.Close() }

func (c *rpcClient) call(method string, params, out any) error {
	return c.callContext(context.Background(), method, params, out)
}

func (c *rpcClient) callContext(ctx context.Context, method string, params, out any) error {
	c.gateOnce.Do(func() { c.gate = make(chan struct{}, 1) })
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(65 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.conn.SetDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = c.conn.SetDeadline(time.Time{})
	}()
	fail := func(err error) error {
		// A late response must never be mistaken for a later call's response.
		_ = c.conn.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return fmt.Errorf("session daemon request failed: %w", err)
	}
	c.nextID++
	req := rpcRequest{ID: c.nextID, Method: method, Origin: c.profile.Origin, Deadline: deadline.UnixMilli()}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	var res rpcResponse
	if err := c.dec.Decode(&res); err != nil {
		return fail(err)
	}
	if res.ID != req.ID {
		return fail(fmt.Errorf("unexpected response ID %d (wanted %d)", res.ID, req.ID))
	}
	if res.Error != "" {
		if res.Auth {
			return &AuthError{Profile: c.profile.Name, Detail: res.Error, Status: res.AuthStatus, URL: res.AuthURL}
		}
		return fmt.Errorf("%s", res.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(res.Result, out)
}

// Relative URLs are resolved here, against the caller's origin: the holder
// would resolve them against its own, and plugins sharing a profile need not
// share an origin (a bitbucket command routed through a jira daemon).

func (c *rpcClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if deadline, ok := req.Context().Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(req.Context(), cancel)
	defer func() { stop(); cancel() }()
	p := doParams{Method: req.Method, Path: ResolveAgainst(c.profile.Origin, req.URL.String()), Headers: map[string]string{}}
	for k := range req.Header {
		p.Headers[k] = req.Header.Get(k)
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		p.Body = body
	}
	var r doResult
	if err := c.callContext(ctx, "do", p, &r); err != nil {
		return nil, err
	}
	hdr := http.Header{}
	for k, v := range r.Headers {
		hdr.Set(k, v)
	}
	outReq := req.Clone(ctx)
	if u, err := parseURL(r.URL); err == nil && r.URL != "" {
		outReq.URL = u
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status)),
		StatusCode:    r.Status,
		Header:        hdr,
		Body:          io.NopCloser(bytes.NewReader(r.Body)),
		ContentLength: int64(len(r.Body)),
		Request:       outReq,
	}, nil
}

func (c *rpcClient) GetJSON(ctx context.Context, path string, out any) error {
	raw, err := Get(ctx, c, path)
	if err != nil {
		return err
	}
	if raw.JSON == nil {
		return fmt.Errorf("expected JSON from %s but got HTTP %d (%s)", path, raw.Status, Snippet(raw.Body, 120))
	}
	if !raw.OK {
		return fmt.Errorf("%s: HTTP %d: %s", path, raw.Status, Snippet(raw.Body, 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw.JSON, out)
}

func (c *rpcClient) Eval(ctx context.Context, js string, arg, out any) error {
	p := evalParams{JS: js}
	if arg != nil {
		b, err := json.Marshal(arg)
		if err != nil {
			return err
		}
		p.Arg = b
	}
	return c.callContext(ctx, "eval", p, out)
}

func (c *rpcClient) Navigate(ctx context.Context, rawurl string) (string, error) {
	var final string
	if err := c.callContext(ctx, "navigate", ResolveAgainst(c.profile.Origin, rawurl), &final); err != nil {
		return "", err
	}
	return final, nil
}

func (c *rpcClient) Cookies(ctx context.Context, rawurl string) ([]Cookie, error) {
	var out []Cookie
	if err := c.callContext(ctx, "cookies", ResolveAgainst(c.profile.Origin, rawurl), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Whoami is answered locally, not by the holder: identity is a property of the
// *plugin's* origin, and the process holding the profile may be a different
// plugin entirely (a teams capture serving a jira command, say).
func (c *rpcClient) Whoami(ctx context.Context) (Identity, error) {
	c.identityMu.Lock()
	if c.hasIdnt {
		id := c.ident
		c.identityMu.Unlock()
		return id, nil
	}
	c.identityMu.Unlock()
	var id Identity
	var err error
	if c.profile.Whoami == nil {
		err = c.callContext(ctx, "whoami", nil, &id)
	} else {
		id, err = c.profile.Whoami(ctx, c)
	}
	if err != nil {
		return Identity{}, err
	}
	c.RememberIdentity(id)
	return id, nil
}

func (c *rpcClient) RememberIdentity(id Identity) {
	if id.Empty() {
		return
	}
	c.identityMu.Lock()
	c.ident, c.hasIdnt = id, true
	c.identityMu.Unlock()
}

func (c *rpcClient) storageState() (json.RawMessage, error) {
	var st json.RawMessage
	if err := c.call("storagestate", nil, &st); err != nil {
		return nil, err
	}
	return st, nil
}

// IdleFor is zero while a working client remains connected, including time
// between its requests. Health probes do not extend the daemon's lifetime.
func (s *Server) IdleFor() time.Duration {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activeRequests > 0 || s.activeClients > 0 {
		return 0
	}
	return time.Since(s.lastActivity)
}

// socketError explains the usual reason a socket cannot be created under a
// custom SESSH_HOME: Unix socket paths are limited to about 100 bytes.
func socketError(err error, path string) error {
	if len(path) < 100 {
		return err
	}
	return fmt.Errorf("%w (the socket path is %d bytes; Unix sockets allow about 100, so use a shorter SESSH_HOME)", err, len(path))
}
