// Package session owns browser profiles and hands plugins an authenticated,
// process-boundary-safe view of one.
//
// The Session interface is deliberately small, and deliberately contains only
// methods whose arguments and results can be serialized: the same interface is
// served in-process (session/browser.go) and over a unix socket to whichever
// process currently holds the profile lock (session/rpc.go). No Playwright type
// may leak through it.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Identity is who the profile is logged in as, as far as one plugin can tell.
type Identity struct {
	AccountID   string `json:"accountId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Username    string `json:"username,omitempty"`
}

func (i Identity) Empty() bool {
	return i.AccountID == "" && i.DisplayName == "" && i.Email == "" && i.Username == ""
}

// Name is the friendliest label available.
func (i Identity) Name() string {
	for _, s := range []string{i.DisplayName, i.Username, i.Email, i.AccountID} {
		if s != "" {
			return s
		}
	}
	return ""
}

// Cookie is a browser cookie, reduced to the fields a Verify function needs.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// Session is the whole contract a plugin sees.
type Session interface {
	// Do performs an authenticated HTTP request against the profile's origin,
	// carrying its cookies. A relative URL resolves against Profile.Origin.
	// Non-2xx responses come back as responses, not errors; only transport and
	// authentication failures are errors.
	Do(ctx context.Context, req *http.Request) (*http.Response, error)

	// GetJSON GETs a path and decodes the JSON body into out.
	GetJSON(ctx context.Context, path string, out any) error

	// Eval runs JavaScript in a page on the origin, for what HTTP can't reach
	// (the teams plugin reads IndexedDB this way). arg is passed to the
	// expression; the result is decoded into out. Both may be nil.
	Eval(ctx context.Context, js string, arg, out any) error

	// Navigate points the session's page at a URL and reports where it landed.
	// Profile.Verify is allowed to use this.
	Navigate(ctx context.Context, rawurl string) (string, error)

	// Cookies returns the cookies the profile would send to rawurl.
	Cookies(ctx context.Context, rawurl string) ([]Cookie, error)

	// Whoami reports the logged-in identity, or an empty Identity when the
	// profile is anonymous.
	Whoami(ctx context.Context) (Identity, error)
}

// ---------------------------------------------------------------------------
// Helpers shared by every plugin
// ---------------------------------------------------------------------------

// Request builds a request for a path relative to the session's origin.
func Request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// Raw is a decoded HTTP response: status, final URL and body, with the JSON
// already parsed when it parsed at all. Plugins branch on Status (404 -> try
// the older API) far more often than they want an error.
type Raw struct {
	Status int
	OK     bool
	URL    string
	Body   []byte
	JSON   json.RawMessage // nil when the body was not JSON
}

// Get performs a GET and returns the decoded response.
func Get(ctx context.Context, s Session, path string) (*Raw, error) {
	req, err := Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	raw := &Raw{
		Status: resp.StatusCode,
		OK:     resp.StatusCode >= 200 && resp.StatusCode < 300,
		Body:   body,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		raw.URL = resp.Request.URL.String()
	}
	if json.Valid(bytes.TrimSpace(body)) && len(bytes.TrimSpace(body)) > 0 {
		raw.JSON = json.RawMessage(bytes.TrimSpace(body))
	}
	return raw, nil
}

// Decode unmarshals a Raw's JSON payload.
func (r *Raw) Decode(out any) error {
	if r.JSON == nil {
		return fmt.Errorf("expected JSON but got HTTP %d (%s)", r.Status, Snippet(r.Body, 120))
	}
	return json.Unmarshal(r.JSON, out)
}

// Snippet is a short, single-line preview of a response body, for error text.
func Snippet(b []byte, n int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ResolveAgainst turns a possibly-relative path into an absolute URL under
// origin. Absolute URLs pass through untouched.
func ResolveAgainst(origin, path string) string {
	if strings.HasPrefix(strings.ToLower(path), "http://") || strings.HasPrefix(strings.ToLower(path), "https://") {
		return path
	}
	base := strings.TrimRight(origin, "/")
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

// IsLoginURL reports whether a URL points to an Atlassian/Microsoft login
// origin or a sign-in page. Verification can stop polling an anonymous API
// once navigation has reached a page that requires the user to sign in.
func IsLoginURL(rawurl string) bool {
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	path := strings.ToLower(u.Path)
	switch {
	case strings.Contains(host, "id.atlassian.com"),
		strings.Contains(host, "login.microsoftonline.com"),
		strings.Contains(host, "login.live.com"):
		return true
	case strings.HasPrefix(path, "/account/signin"), strings.HasPrefix(path, "/login"):
		return true
	}
	return false
}
