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
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
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
}

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
	})
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	// Page work for this client happens on a page of its own, created on first
	// use and closed with the connection. HTTP needs no page at all, so a
	// client that only reads APIs never costs one.
	var (
		client  *browserSession
		release func()
	)
	defer func() {
		if release != nil {
			release()
		}
	}()
	pageSession := func() (*browserSession, error) {
		if client != nil {
			return client, nil
		}
		c, closer, err := s.sess.fork()
		if err != nil {
			return nil, err
		}
		client, release = c, closer
		return client, nil
	}

	r := bufio.NewReaderSize(conn, 1<<20)
	w := bufio.NewWriterSize(conn, 1<<20)
	dec := json.NewDecoder(r)
	for {
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			return
		}
		res := s.dispatch(&req, pageSession)
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
	ctx := context.Background()
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
	conn    net.Conn
	dec     *json.Decoder
	mu      sync.Mutex
	nextID  int
	profile Profile
}

var _ Session = (*rpcClient)(nil)

func dialProfile(name string) (*rpcClient, error) {
	conn, err := net.DialTimeout("unix", SocketPath(name), 2*time.Second)
	if err != nil {
		return nil, err
	}
	c := &rpcClient{conn: conn, dec: json.NewDecoder(bufio.NewReaderSize(conn, 1<<20))}
	if err := c.call("ping", nil, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *rpcClient) Close() error { return c.conn.Close() }

func (c *rpcClient) call(method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	req := rpcRequest{ID: c.nextID, Method: method}
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
		return err
	}
	var res rpcResponse
	if err := c.dec.Decode(&res); err != nil {
		return fmt.Errorf("session daemon closed the connection: %w", err)
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
	if err := c.call("do", p, &r); err != nil {
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

func (c *rpcClient) Eval(_ context.Context, js string, arg, out any) error {
	p := evalParams{JS: js}
	if arg != nil {
		b, err := json.Marshal(arg)
		if err != nil {
			return err
		}
		p.Arg = b
	}
	return c.call("eval", p, out)
}

func (c *rpcClient) Navigate(_ context.Context, rawurl string) (string, error) {
	var final string
	if err := c.call("navigate", ResolveAgainst(c.profile.Origin, rawurl), &final); err != nil {
		return "", err
	}
	return final, nil
}

func (c *rpcClient) Cookies(_ context.Context, rawurl string) ([]Cookie, error) {
	var out []Cookie
	if err := c.call("cookies", ResolveAgainst(c.profile.Origin, rawurl), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Whoami is answered locally, not by the holder: identity is a property of the
// *plugin's* origin, and the process holding the profile may be a different
// plugin entirely (a teams capture serving a jira command, say).
func (c *rpcClient) Whoami(ctx context.Context) (Identity, error) {
	if c.profile.Whoami == nil {
		var id Identity
		if err := c.call("whoami", nil, &id); err != nil {
			return Identity{}, err
		}
		return id, nil
	}
	return c.profile.Whoami(ctx, c)
}

func (c *rpcClient) storageState() (json.RawMessage, error) {
	var st json.RawMessage
	if err := c.call("storagestate", nil, &st); err != nil {
		return nil, err
	}
	return st, nil
}

// IdleFor is zero while any client request is in flight. Health probes do not
// extend the lifetime of an otherwise idle daemon.
func (s *Server) IdleFor() time.Duration {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activeRequests > 0 {
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
