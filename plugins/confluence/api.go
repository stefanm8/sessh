package confluence

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/stefanm8/sessh/session"
)

// Confluence Cloud REST helpers.
//
// The primary surface is v2 (/wiki/api/v2/…); where v2 has no equivalent — or
// isn't available on a deployment — we fall back to v1 (/wiki/rest/api/…).
// Search is v1 only: v2 has no CQL endpoint.
//
// Both APIs hand back a `_links.next`, but whether it carries the /wiki mount
// point isn't guaranteed — so we never follow it. We pull the opaque `cursor`
// out of it (or track `start` ourselves) and rebuild a URL we know is right.

const (
	pageLen = 100
	runaway = 200 // max pagination rounds, a loop guard
)

// apiErr carries the HTTP status so callers can tell "this deployment doesn't
// support what you asked for" (400) from a real failure.
type apiErr struct {
	Status int
	Msg    string
}

func (e *apiErr) Error() string { return e.Msg }

func statusOf(err error) int {
	var e *apiErr
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func fromResponse(raw *session.Raw, path string) (json.RawMessage, error) {
	if raw.JSON == nil {
		return nil, &apiErr{Status: raw.Status, Msg: fmt.Sprintf(
			"expected JSON from %s but got HTTP %d. The session may have expired — try: sessh auth login confluence",
			path, raw.Status)}
	}
	if !raw.OK {
		return nil, &apiErr{Status: raw.Status, Msg: errText(raw.JSON, raw.Status, path)}
	}
	return raw.JSON, nil
}

func errText(payload json.RawMessage, status int, path string) string {
	prefix := "Confluence API error for " + path
	var body struct {
		Errors []struct {
			Title   string `json:"title"`
			Detail  string `json:"detail"`
			Message string `json:"message"`
		} `json:"errors"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &body) == nil {
		var msgs []string
		for _, e := range body.Errors {
			if m := cmp.Or(e.Title, e.Detail, e.Message); m != "" {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) > 0 {
			return prefix + ": " + strings.Join(msgs, "; ")
		}
		if body.Message != "" {
			return prefix + ": " + body.Message
		}
	}
	return fmt.Sprintf("%s: HTTP %d", prefix, status)
}

func getJSON(ctx context.Context, s session.Session, path string) (json.RawMessage, error) {
	raw, err := session.Get(ctx, s, path)
	if err != nil {
		return nil, err
	}
	return fromResponse(raw, path)
}

// tryGet returns nil, nil for "this endpoint isn't here", so callers can fall
// back to the older API.
func tryGet(ctx context.Context, s session.Session, path string) (json.RawMessage, error) {
	raw, err := session.Get(ctx, s, path)
	if err != nil {
		return nil, err
	}
	switch raw.Status {
	case 404, 405, 410:
		return nil, nil
	}
	return fromResponse(raw, path)
}

// Client caches the space id/key lookups a listing needs.
type Client struct {
	S session.Session

	mu       sync.Mutex
	spaceIDs map[string]string // key -> id
	spaceKey map[string]string // id  -> key
}

// New wraps a session with the caches the page/space commands share.
func New(s session.Session) *Client {
	return &Client{S: s, spaceIDs: map[string]string{}, spaceKey: map[string]string{}}
}

// pageThroughV2 walks v2's opaque cursor pagination. It returns ok=false when
// the endpoint 404s, meaning "fall back to v1".
func (c *Client) pageThroughV2(ctx context.Context, basePath string, max, limit int) ([]json.RawMessage, bool, error) {
	if limit == 0 {
		limit = pageLen
	}
	sep := "?"
	if strings.Contains(basePath, "?") {
		sep = "&"
	}
	var out []json.RawMessage
	cursor := ""
	for i := 0; i < runaway; i++ {
		u := fmt.Sprintf("%s%slimit=%d", basePath, sep, limit)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		payload, err := tryGet(ctx, c.S, u)
		if err != nil {
			return nil, false, err
		}
		if payload == nil {
			return nil, false, nil
		}
		var envelope struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, false, err
		}
		for _, r := range envelope.Results {
			out = append(out, r)
			if max > 0 && len(out) >= max {
				return out, true, nil
			}
		}
		if envelope.Links.Next == "" || len(envelope.Results) == 0 {
			break
		}
		m := cursorRe.FindStringSubmatch(envelope.Links.Next)
		if m == nil {
			break
		}
		next, err := url.QueryUnescape(m[1])
		if err != nil {
			break
		}
		cursor = next
	}
	return out, true, nil
}

var cursorRe = regexp.MustCompile(`[?&]cursor=([^&]+)`)

// pageThroughV2Optional retries without the optional query params (sorting,
// description expansion) if this deployment rejects one with a 400. A nicety
// should never fail a whole listing.
func (c *Client) pageThroughV2Optional(ctx context.Context, basePath, extra string, max, limit int) ([]json.RawMessage, bool, error) {
	sep := "?"
	if strings.Contains(basePath, "?") {
		sep = "&"
	}
	path := basePath
	if extra != "" {
		path = basePath + sep + extra
	}
	out, ok, err := c.pageThroughV2(ctx, path, max, limit)
	if err != nil && extra != "" && statusOf(err) == 400 {
		return c.pageThroughV2(ctx, basePath, max, limit)
	}
	return out, ok, err
}

// pageThroughV1 walks v1's start/limit offset pagination.
func (c *Client) pageThroughV1(ctx context.Context, basePath string, max, limit int) ([]json.RawMessage, error) {
	if limit == 0 {
		limit = pageLen
	}
	sep := "?"
	if strings.Contains(basePath, "?") {
		sep = "&"
	}
	var out []json.RawMessage
	start := 0
	for i := 0; i < runaway; i++ {
		payload, err := getJSON(ctx, c.S, fmt.Sprintf("%s%slimit=%d&start=%d", basePath, sep, limit, start))
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Results   []json.RawMessage `json:"results"`
			TotalSize *int              `json:"totalSize"`
			Size      *int              `json:"size"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, err
		}
		for _, r := range envelope.Results {
			out = append(out, r)
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		start += len(envelope.Results)
		if len(envelope.Results) == 0 || len(envelope.Results) < limit {
			break
		}
		if envelope.TotalSize != nil && start >= *envelope.TotalSize {
			break
		}
		if envelope.Size != nil && *envelope.Size < limit {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Normalisation — v1 and v2 describe the same things differently.
// ---------------------------------------------------------------------------

// Space is the normalized shape of a space in either API version.
type Space struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Description string            `json:"description"`
	HomepageID  string            `json:"homepageId,omitempty"`
	Links       map[string]string `json:"-"`
	Raw         json.RawMessage   `json:"-"`
}

// Page is the normalized shape of a page in either API version.
type Page struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Status     string            `json:"status,omitempty"`
	Type       string            `json:"type,omitempty"`
	SpaceID    string            `json:"spaceId,omitempty"`
	SpaceKey   string            `json:"spaceKey,omitempty"`
	ParentID   string            `json:"parentId,omitempty"`
	Version    int               `json:"version,omitempty"`
	Updated    string            `json:"updated,omitempty"`
	UpdatedBy  string            `json:"updatedBy,omitempty"`
	AuthorID   string            `json:"authorId,omitempty"`
	CreatedAt  string            `json:"createdAt,omitempty"`
	Body       string            `json:"-"`
	BodyFormat string            `json:"bodyFormat,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Ancestors  []Ancestor        `json:"ancestors,omitempty"`
	Links      map[string]string `json:"-"`
	Raw        json.RawMessage   `json:"-"`
}

// Ancestor is one step of a page's breadcrumb.
type Ancestor struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// rawSpace/rawPage model both API shapes at once; whichever fields are present
// win.
type rawSpace struct {
	ID          any    `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description struct {
		Plain struct {
			Value string `json:"value"`
		} `json:"plain"`
		View struct {
			Value string `json:"value"`
		} `json:"view"`
	} `json:"description"`
	HomepageID any `json:"homepageId"`
	Homepage   *struct {
		ID any `json:"id"`
	} `json:"homepage"`
	Links map[string]string `json:"_links"`
}

func normalizeSpace(raw json.RawMessage) *Space {
	var r rawSpace
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	desc := cmp.Or(r.Description.Plain.Value, r.Description.View.Value)
	home := idString(r.HomepageID)
	if home == "" && r.Homepage != nil {
		home = idString(r.Homepage.ID)
	}
	return &Space{
		ID: idString(r.ID), Key: r.Key, Name: r.Name, Type: r.Type, Status: r.Status,
		Description: strings.TrimSpace(desc), HomepageID: home,
		Links: r.Links, Raw: raw,
	}
}

type rawPage struct {
	ID      any    `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Type    string `json:"type"`
	SpaceID any    `json:"spaceId"`
	Space   *struct {
		ID  any    `json:"id"`
		Key string `json:"key"`
	} `json:"space"`
	SpaceKey string `json:"spaceKey"`
	ParentID any    `json:"parentId"`
	Version  struct {
		Number    int    `json:"number"`
		CreatedAt string `json:"createdAt"`
		When      string `json:"when"`
		AuthorID  string `json:"authorId"`
		By        struct {
			DisplayName string `json:"displayName"`
		} `json:"by"`
	} `json:"version"`
	AuthorID  string `json:"authorId"`
	CreatedAt string `json:"createdAt"`
	History   struct {
		CreatedDate string `json:"createdDate"`
		LastUpdated struct {
			When string `json:"when"`
			By   struct {
				DisplayName string `json:"displayName"`
			} `json:"by"`
		} `json:"lastUpdated"`
	} `json:"history"`
	Ancestors []struct {
		ID    any    `json:"id"`
		Title string `json:"title"`
	} `json:"ancestors"`
	Metadata struct {
		Labels struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
	LabelsV2 struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	} `json:"labels"`
	Body map[string]struct {
		Value string `json:"value"`
	} `json:"body"`
	Links map[string]string `json:"_links"`
}

func normalizePage(raw json.RawMessage) *Page {
	var r rawPage
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	p := &Page{
		ID: idString(r.ID), Title: r.Title, Status: r.Status, Type: r.Type,
		SpaceID: idString(r.SpaceID), SpaceKey: r.SpaceKey, ParentID: idString(r.ParentID),
		Version:   r.Version.Number,
		Updated:   cmp.Or(r.Version.CreatedAt, r.Version.When, r.History.LastUpdated.When),
		UpdatedBy: cmp.Or(r.Version.By.DisplayName, r.History.LastUpdated.By.DisplayName),
		AuthorID:  cmp.Or(r.AuthorID, r.Version.AuthorID),
		CreatedAt: cmp.Or(r.CreatedAt, r.History.CreatedDate),
		Links:     r.Links, Raw: raw,
	}
	if p.Type == "" {
		p.Type = "page"
	}
	if r.Space != nil {
		if p.SpaceID == "" {
			p.SpaceID = idString(r.Space.ID)
		}
		if p.SpaceKey == "" {
			p.SpaceKey = r.Space.Key
		}
	}
	for _, a := range r.Ancestors {
		p.Ancestors = append(p.Ancestors, Ancestor{ID: idString(a.ID), Title: a.Title})
	}
	labels := r.LabelsV2.Results
	if len(labels) == 0 {
		labels = r.Metadata.Labels.Results
	}
	for _, l := range labels {
		if l.Name != "" {
			p.Labels = append(p.Labels, l.Name)
		}
	}
	return p
}

func bodyOf(raw json.RawMessage, format string) string {
	var r rawPage
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	key := "storage"
	switch format {
	case "view", "export_view", "atlas_doc_format":
		key = format
	}
	if b, ok := r.Body[key]; ok {
		return b.Value
	}
	return ""
}

func idString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%.0f", t), ".0")
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// ---------------------------------------------------------------------------
// Spaces
// ---------------------------------------------------------------------------

func (c *Client) ListSpaces(ctx context.Context, max int) ([]*Space, error) {
	raws, ok, err := c.pageThroughV2Optional(ctx, "/wiki/api/v2/spaces", "sort=name&description-format=plain", max, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		raws, err = c.pageThroughV1(ctx, "/wiki/rest/api/space?expand=description.plain,homepage", max, 0)
		if err != nil {
			return nil, err
		}
	}
	out := make([]*Space, 0, len(raws))
	for _, raw := range raws {
		if s := normalizeSpace(raw); s != nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *Client) GetSpace(ctx context.Context, key string) (*Space, error) {
	payload, err := tryGet(ctx, c.S,
		"/wiki/api/v2/spaces?keys="+url.QueryEscape(key)+"&description-format=plain&limit=1")
	if err != nil {
		return nil, err
	}
	if payload != nil {
		var envelope struct {
			Results []json.RawMessage `json:"results"`
		}
		if json.Unmarshal(payload, &envelope) == nil && len(envelope.Results) > 0 {
			if s := normalizeSpace(envelope.Results[0]); s != nil {
				return s, nil
			}
		}
	}
	payload, err = tryGet(ctx, c.S,
		"/wiki/rest/api/space/"+url.PathEscape(key)+"?expand=description.plain,homepage")
	if err != nil {
		return nil, err
	}
	if payload != nil {
		if s := normalizeSpace(payload); s != nil {
			return s, nil
		}
	}
	return nil, fmt.Errorf("space not found: %s", key)
}

// SpaceIDFor maps a space key to the numeric id v2 filters by.
func (c *Client) SpaceIDFor(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	c.mu.Lock()
	if id, ok := c.spaceIDs[key]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	s, err := c.GetSpace(ctx, key)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.spaceIDs[key] = s.ID
	if s.Key != "" {
		c.spaceKey[s.ID] = s.Key
	}
	c.mu.Unlock()
	return s.ID, nil
}

// SpaceKeyFor maps a numeric space id back to its key (v2 pages carry only the
// id, humans want the key). Best-effort and cached.
func (c *Client) SpaceKeyFor(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	c.mu.Lock()
	if key, ok := c.spaceKey[id]; ok {
		c.mu.Unlock()
		return key
	}
	c.mu.Unlock()

	key := ""
	if payload, err := tryGet(ctx, c.S, "/wiki/api/v2/spaces/"+url.PathEscape(id)); err == nil && payload != nil {
		var sp struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(payload, &sp) == nil {
			key = sp.Key
		}
	}
	c.mu.Lock()
	c.spaceKey[id] = key
	c.mu.Unlock()
	return key
}

// ---------------------------------------------------------------------------
// Pages
// ---------------------------------------------------------------------------

// ListPagesOptions filters a page listing.
type ListPagesOptions struct {
	Space  string
	Max    int
	Sort   string
	Status string
}

func (c *Client) ListPages(ctx context.Context, opts ListPagesOptions) ([]*Page, error) {
	if opts.Sort == "" {
		opts.Sort = "-modified-date"
	}
	if opts.Status == "" {
		opts.Status = "current"
	}
	base := "/wiki/api/v2/pages?status=" + url.QueryEscape(opts.Status)
	if opts.Space != "" {
		id, err := c.SpaceIDFor(ctx, opts.Space)
		if err != nil {
			return nil, err
		}
		if id != "" {
			base += "&space-id=" + url.QueryEscape(id)
		}
	}
	raws, ok, err := c.pageThroughV2Optional(ctx, base, "sort="+url.QueryEscape(opts.Sort), opts.Max, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		v1 := "/wiki/rest/api/content?type=page&expand=space,version,history.lastUpdated"
		if opts.Space != "" {
			v1 += "&spaceKey=" + url.QueryEscape(opts.Space)
		}
		raws, err = c.pageThroughV1(ctx, v1, opts.Max, 0)
		if err != nil {
			return nil, err
		}
	}
	out := make([]*Page, 0, len(raws))
	for _, raw := range raws {
		if p := normalizePage(raw); p != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// GetPage fetches one page including its body.
//
// If storage comes back empty (some new-editor content does this) we retry with
// the server-rendered view HTML, which the Markdown converter also understands.
func (c *Client) GetPage(ctx context.Context, id, format string) (*Page, error) {
	if format == "" {
		format = "storage"
	}
	var (
		p      *Page
		body   string
		actual = format
	)

	payload, err := tryGet(ctx, c.S,
		"/wiki/api/v2/pages/"+url.PathEscape(id)+"?body-format="+url.QueryEscape(format))
	if err != nil {
		return nil, err
	}
	if payload != nil {
		p = normalizePage(payload)
		body = bodyOf(payload, format)
	} else {
		expand := "body.storage,version,space,ancestors,history.lastUpdated,metadata.labels"
		if format == "view" {
			expand = "body.view,version,space,ancestors,history.lastUpdated,metadata.labels"
		}
		payload, err = tryGet(ctx, c.S,
			"/wiki/rest/api/content/"+url.PathEscape(id)+"?expand="+url.QueryEscape(expand))
		if err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, fmt.Errorf("page not found: %s", id)
		}
		p = normalizePage(payload)
		body = bodyOf(payload, format)
	}
	if p == nil {
		return nil, fmt.Errorf("page not found: %s", id)
	}

	if strings.TrimSpace(body) == "" && format == "storage" {
		if alt, err := tryGet(ctx, c.S, "/wiki/rest/api/content/"+url.PathEscape(id)+"?expand=body.view"); err == nil && alt != nil {
			if v := bodyOf(alt, "view"); strings.TrimSpace(v) != "" {
				body, actual = v, "view"
			}
		}
	}

	p.Body = body
	p.BodyFormat = actual
	if p.SpaceKey == "" && p.SpaceID != "" {
		p.SpaceKey = c.SpaceKeyFor(ctx, p.SpaceID)
	}
	return p, nil
}

func (c *Client) GetChildren(ctx context.Context, id string, max int) ([]*Page, error) {
	raws, ok, err := c.pageThroughV2(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/children", max, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		raws, err = c.pageThroughV1(ctx,
			"/wiki/rest/api/content/"+url.PathEscape(id)+"/child/page?expand=version,space", max, 0)
		if err != nil {
			return nil, err
		}
	}
	out := make([]*Page, 0, len(raws))
	for _, raw := range raws {
		if p := normalizePage(raw); p != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// GetAncestors returns the breadcrumb, root-first. Best-effort: it never fails
// a page view.
func (c *Client) GetAncestors(ctx context.Context, id string) []Ancestor {
	if payload, err := tryGet(ctx, c.S, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/ancestors?limit=25"); err == nil && payload != nil {
		var envelope struct {
			Results []struct {
				ID    any    `json:"id"`
				Title string `json:"title"`
			} `json:"results"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			out := make([]Ancestor, 0, len(envelope.Results))
			for _, a := range envelope.Results {
				out = append(out, Ancestor{ID: idString(a.ID), Title: a.Title})
			}
			return out
		}
	}
	if payload, err := tryGet(ctx, c.S, "/wiki/rest/api/content/"+url.PathEscape(id)+"?expand=ancestors"); err == nil && payload != nil {
		if p := normalizePage(payload); p != nil {
			return p.Ancestors
		}
	}
	return nil
}

func (c *Client) GetLabels(ctx context.Context, id string) []string {
	payload, err := tryGet(ctx, c.S, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/labels?limit=50")
	if err != nil || payload == nil {
		return nil
	}
	var envelope struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil
	}
	var out []string
	for _, l := range envelope.Results {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// Attachment is a file attached to a page.
type Attachment struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	MediaType    string `json:"mediaType,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Updated      string `json:"updated,omitempty"`
	DownloadLink string `json:"downloadLink,omitempty"`
}

func (c *Client) GetAttachments(ctx context.Context, id string, max int) ([]Attachment, error) {
	raws, ok, err := c.pageThroughV2Optional(ctx,
		"/wiki/api/v2/pages/"+url.PathEscape(id)+"/attachments", "sort=-modified-date", max, 50)
	if err != nil {
		return nil, err
	}
	if !ok {
		raws, err = c.pageThroughV1(ctx,
			"/wiki/rest/api/content/"+url.PathEscape(id)+"/child/attachment?expand=version,metadata", max, 50)
		if err != nil {
			return nil, err
		}
	}
	out := make([]Attachment, 0, len(raws))
	for _, raw := range raws {
		var a struct {
			ID        any    `json:"id"`
			Title     string `json:"title"`
			MediaType string `json:"mediaType"`
			FileSize  *int64 `json:"fileSize"`
			Metadata  struct {
				MediaType string `json:"mediaType"`
			} `json:"metadata"`
			Extensions struct {
				FileSize *int64 `json:"fileSize"`
			} `json:"extensions"`
			Version struct {
				CreatedAt string `json:"createdAt"`
				When      string `json:"when"`
			} `json:"version"`
			Links map[string]string `json:"_links"`
		}
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		var size int64
		if a.FileSize != nil {
			size = *a.FileSize
		} else if a.Extensions.FileSize != nil {
			size = *a.Extensions.FileSize
		}
		out = append(out, Attachment{
			ID:           idString(a.ID),
			Title:        a.Title,
			MediaType:    cmp.Or(a.MediaType, a.Metadata.MediaType),
			Size:         size,
			Updated:      cmp.Or(a.Version.CreatedAt, a.Version.When),
			DownloadLink: cmp.Or(a.Links["download"], a.Links["webui"]),
		})
	}
	return out, nil
}

// ResolveUsers maps account ids to display names for @mentions. Bounded and
// best-effort: an unresolvable id renders as a generic @user rather than
// failing the page.
func (c *Client) ResolveUsers(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) > 50 {
		ids = ids[:50]
	}
	for _, id := range ids {
		payload, err := getJSON(ctx, c.S, "/wiki/rest/api/user?accountId="+url.QueryEscape(id))
		if err != nil {
			continue
		}
		var u struct {
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal(payload, &u) == nil && u.DisplayName != "" {
			out[id] = u.DisplayName
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Search (CQL — v1 only)
// ---------------------------------------------------------------------------

// EscapeCQL escapes a value for use inside a quoted CQL string.
func EscapeCQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// BuildCQL composes a query.
//
// `siteSearch` is the relevance-ranked matcher and only exists on the search
// endpoint; `text` is the portable one. Free text already sorts by relevance,
// so we only add an ORDER BY when the query is pure filters.
func BuildCQL(text, space, typ, matcher string) string {
	if matcher == "" {
		matcher = "siteSearch"
	}
	if typ == "" {
		typ = "page"
	}
	var clauses []string
	if text != "" {
		clauses = append(clauses, fmt.Sprintf(`%s ~ "%s"`, matcher, EscapeCQL(text)))
	}
	if space != "" {
		clauses = append(clauses, fmt.Sprintf(`space = "%s"`, EscapeCQL(space)))
	}
	clauses = append(clauses, fmt.Sprintf(`type = "%s"`, EscapeCQL(typ)))
	where := strings.Join(clauses, " AND ")
	if text != "" {
		return where
	}
	return where + " ORDER BY lastmodified DESC"
}

var hlRe = regexp.MustCompile(`@@@(?:end)?hl@@@`)

// StripHL removes the search endpoint's highlight markers.
func StripHL(s string) string { return hlRe.ReplaceAllString(s, "") }

// Hit is one search result.
type Hit struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	Space        string `json:"space,omitempty"`
	SpaceKey     string `json:"spaceKey,omitempty"`
	Excerpt      string `json:"excerpt,omitempty"`
	URL          string `json:"url,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

func normalizeHit(raw json.RawMessage) Hit {
	var r struct {
		Title   string `json:"title"`
		Excerpt string `json:"excerpt"`
		URL     string `json:"url"`
		Content struct {
			ID    any    `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
			Space struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"space"`
			Version struct {
				When string `json:"when"`
			} `json:"version"`
			Links map[string]string `json:"_links"`
		} `json:"content"`
		EntityType            string `json:"entityType"`
		LastModified          string `json:"lastModified"`
		ResultGlobalContainer struct {
			Title string `json:"title"`
		} `json:"resultGlobalContainer"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return Hit{}
	}
	return Hit{
		ID:           idString(r.Content.ID),
		Type:         cmp.Or(r.Content.Type, r.EntityType),
		Title:        StripHL(cmp.Or(r.Title, r.Content.Title)),
		Space:        StripHL(cmp.Or(r.ResultGlobalContainer.Title, r.Content.Space.Name)),
		SpaceKey:     r.Content.Space.Key,
		Excerpt:      strings.TrimSpace(whitespaceRe.ReplaceAllString(StripHL(r.Excerpt), " ")),
		URL:          cmp.Or(r.URL, r.Content.Links["webui"]),
		LastModified: cmp.Or(r.LastModified, r.Content.Version.When),
	}
}

func (c *Client) Search(ctx context.Context, cql string, max int) ([]Hit, error) {
	const limit = 50
	var out []Hit
	start := 0
	for i := 0; i < runaway; i++ {
		payload, err := getJSON(ctx, c.S, fmt.Sprintf("/wiki/rest/api/search?cql=%s&limit=%d&start=%d",
			url.QueryEscape(cql), limit, start))
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Results   []json.RawMessage `json:"results"`
			TotalSize *int              `json:"totalSize"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, err
		}
		for _, r := range envelope.Results {
			out = append(out, normalizeHit(r))
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		start += len(envelope.Results)
		if len(envelope.Results) == 0 || len(envelope.Results) < limit {
			break
		}
		if envelope.TotalSize != nil && start >= *envelope.TotalSize {
			break
		}
	}
	return out, nil
}

// SearchText degrades gracefully: `siteSearch` is rejected on some
// deployments, where `text` gives the same result set with worse ranking.
func (c *Client) SearchText(ctx context.Context, text, space, typ string, max int) ([]Hit, error) {
	hits, err := c.Search(ctx, BuildCQL(text, space, typ, "siteSearch"), max)
	if err != nil && text != "" && statusOf(err) == 400 {
		return c.Search(ctx, BuildCQL(text, space, typ, "text"), max)
	}
	return hits, err
}

// SearchContent returns Content objects (not search hits) — used for exact
// title lookups where we want the page id.
func (c *Client) SearchContent(ctx context.Context, cql string, max int) ([]*Page, error) {
	if max == 0 {
		max = 25
	}
	payload, err := tryGet(ctx, c.S, fmt.Sprintf("/wiki/rest/api/content/search?cql=%s&limit=%d&expand=space,version",
		url.QueryEscape(cql), max))
	if err != nil || payload == nil {
		return nil, err
	}
	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, nil
	}
	out := make([]*Page, 0, len(envelope.Results))
	for _, raw := range envelope.Results {
		if p := normalizePage(raw); p != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reference resolution: URL / id / tiny link / SPACE:Title / free text -> id
// ---------------------------------------------------------------------------

// Resolved is a reference that has been turned into a real page id.
type Resolved struct {
	ID    string
	Space string
	Page  *Page
}

func (c *Client) ResolveRef(ctx context.Context, input string) (*Resolved, error) {
	ref := ParseRef(input)
	if ref == nil {
		return nil, fmt.Errorf("missing page reference")
	}

	switch ref.Kind {
	case RefID:
		return &Resolved{ID: ref.ID, Space: ref.Space}, nil

	case RefSpace:
		return nil, fmt.Errorf("%q points at a space, not a page. Try: sessh confluence space view %s", input, ref.Space)

	case RefTiny:
		// Only a redirect can tell us what a tiny link means; follow it and
		// re-parse where we landed.
		raw, err := session.Get(ctx, c.S, ref.Path)
		if err != nil {
			return nil, err
		}
		if again := ParseRef(raw.URL); again != nil && again.Kind == RefID {
			return &Resolved{ID: again.ID, Space: again.Space}, nil
		}
		return nil, fmt.Errorf("could not expand the tiny link %q (it redirected to %s)", input, raw.URL)
	}

	// Title or free text: try an exact title match first, then relevance search.
	title := ref.Title
	if title == "" {
		title = ref.Text
	}
	space := ref.Space
	exactCQL := fmt.Sprintf(`type = "page" AND title = "%s"`, EscapeCQL(title))
	if space != "" {
		exactCQL += fmt.Sprintf(` AND space = "%s"`, EscapeCQL(space))
	}
	if exact, err := c.SearchContent(ctx, exactCQL, 5); err == nil && len(exact) > 0 {
		return &Resolved{ID: exact[0].ID, Space: cmp.Or(exact[0].SpaceKey, space), Page: exact[0]}, nil
	}
	hits, err := c.SearchText(ctx, title, space, "page", 5)
	if err != nil {
		return nil, err
	}
	if len(hits) > 0 && hits[0].ID != "" {
		return &Resolved{ID: hits[0].ID, Space: cmp.Or(hits[0].SpaceKey, space)}, nil
	}

	hint := ""
	if space == "" {
		hint = ` Try 'sessh confluence search "<text>"' to find it.`
	}
	return nil, fmt.Errorf("no Confluence page found for %q.%s", input, hint)
}
