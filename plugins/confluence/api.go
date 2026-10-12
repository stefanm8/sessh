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

type pagination int

const (
	offsetPages pagination = iota
	cursorPages
	searchPages // Search uses totalSize, not the content API's size field.
)

type resultPage struct {
	Results   []json.RawMessage `json:"results"`
	TotalSize *int              `json:"totalSize"`
	Size      *int              `json:"size"`
	Links     struct {
		Next string `json:"next"`
	} `json:"_links"`
}

func withQuery(path, query string) string {
	if query == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + query
}

// paginate shares limits, decoding and loop guards across both APIs. A false
// supported result means a v2 endpoint is unavailable and v1 should be tried.
func (c *Client) paginate(ctx context.Context, base string, max, limit int, mode pagination) ([]json.RawMessage, bool, error) {
	if limit == 0 {
		limit = pageLen
	}
	base = withQuery(base, fmt.Sprintf("limit=%d", limit))
	get := getJSON
	if mode == cursorPages {
		get = tryGet
	}
	var out []json.RawMessage
	cursor, start := "", 0
	for i := 0; i < runaway; i++ {
		path := base
		if mode != cursorPages {
			path = withQuery(base, fmt.Sprintf("start=%d", start))
		} else if cursor != "" {
			path = withQuery(base, "cursor="+url.QueryEscape(cursor))
		}
		payload, err := get(ctx, c.S, path)
		if err != nil || payload == nil {
			return nil, false, err
		}
		var page resultPage
		if err := json.Unmarshal(payload, &page); err != nil {
			return nil, false, err
		}
		out = append(out, page.Results...)
		if max > 0 && len(out) >= max {
			return out[:max], true, nil
		}
		if len(page.Results) == 0 {
			break
		}
		if mode == cursorPages {
			m := cursorRe.FindStringSubmatch(page.Links.Next)
			if m == nil {
				break
			}
			cursor, err = url.QueryUnescape(m[1])
			if err != nil {
				break
			}
		} else {
			start += len(page.Results)
			if len(page.Results) < limit || (page.TotalSize != nil && start >= *page.TotalSize) ||
				(mode == offsetPages && page.Size != nil && *page.Size < limit) {
				break
			}
		}
	}
	return out, true, nil
}

var cursorRe = regexp.MustCompile(`[?&]cursor=([^&]+)`)

// list retries unsupported optional v2 parameters, then falls back to v1 when
// the endpoint itself is unavailable.
func (c *Client) list(ctx context.Context, v2, extra, v1 string, max, limit int) ([]json.RawMessage, error) {
	out, ok, err := c.paginate(ctx, withQuery(v2, extra), max, limit, cursorPages)
	if err != nil && extra != "" && statusOf(err) == 400 {
		out, ok, err = c.paginate(ctx, v2, max, limit, cursorPages)
	}
	if err == nil && !ok {
		out, _, err = c.paginate(ctx, v1, max, limit, offsetPages)
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Normalisation — v1 and v2 describe the same things differently.
// ---------------------------------------------------------------------------

// Space is the normalized shape of a space in either API version.
type Space struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description string `json:"description"`
	HomepageID  string `json:"homepageId,omitempty"`
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
		Links:     r.Links,
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
		return fmt.Sprintf("%.0f", t)
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
	raws, err := c.list(ctx, "/wiki/api/v2/spaces", "sort=name&description-format=plain",
		"/wiki/rest/api/space?expand=description.plain,homepage", max, 0)
	if err != nil {
		return nil, err
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
	v1 := "/wiki/rest/api/content?type=page&expand=space,version,history.lastUpdated"
	if opts.Space != "" {
		v1 += "&spaceKey=" + url.QueryEscape(opts.Space)
	}
	raws, err := c.list(ctx, base, "sort="+url.QueryEscape(opts.Sort), v1, opts.Max, 0)
	if err != nil {
		return nil, err
	}
	return normalizePages(raws), nil
}

func normalizePages(raws []json.RawMessage) []*Page {
	out := make([]*Page, 0, len(raws))
	for _, raw := range raws {
		if p := normalizePage(raw); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// GetPage fetches one page including its body.
//
// If storage comes back empty (some new-editor content does this) we retry with
// the server-rendered view HTML, which the Markdown converter also understands.
func (c *Client) GetPage(ctx context.Context, id, format string) (*Page, error) {
	if format == "" {
		format = "storage"
	}
	payload, err := tryGet(ctx, c.S,
		"/wiki/api/v2/pages/"+url.PathEscape(id)+"?body-format="+url.QueryEscape(format))
	if err != nil {
		return nil, err
	}
	if payload == nil {
		bodyFormat := "storage"
		if format == "view" {
			bodyFormat = "view"
		}
		expand := "body." + bodyFormat + ",version,space,ancestors,history.lastUpdated,metadata.labels"
		payload, err = tryGet(ctx, c.S,
			"/wiki/rest/api/content/"+url.PathEscape(id)+"?expand="+url.QueryEscape(expand))
		if err != nil {
			return nil, err
		}
	}
	p := normalizePage(payload)
	if p == nil {
		return nil, fmt.Errorf("page not found: %s", id)
	}
	body, actual := bodyOf(payload, format), format

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
	raws, err := c.list(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/children", "",
		"/wiki/rest/api/content/"+url.PathEscape(id)+"/child/page?expand=version,space", max, 0)
	if err != nil {
		return nil, err
	}
	return normalizePages(raws), nil
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
	raws, err := c.list(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/attachments", "sort=-modified-date",
		"/wiki/rest/api/content/"+url.PathEscape(id)+"/child/attachment?expand=version,metadata", max, 50)
	if err != nil {
		return nil, err
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
	raws, _, err := c.paginate(ctx, "/wiki/rest/api/search?cql="+url.QueryEscape(cql), max, 50, searchPages)
	if err != nil {
		return nil, err
	}
	var out []Hit
	for _, raw := range raws {
		out = append(out, normalizeHit(raw))
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
	return normalizePages(envelope.Results), nil
}

// ---------------------------------------------------------------------------
// Reference resolution: URL / id / tiny link / SPACE:Title / free text -> id
// ---------------------------------------------------------------------------

// ResolveRef turns a URL, id, tiny link or title into a page id.
func (c *Client) ResolveRef(ctx context.Context, input string) (string, error) {
	ref := ParseRef(input)
	if ref == nil {
		return "", fmt.Errorf("missing page reference")
	}

	switch ref.Kind {
	case RefID:
		return ref.ID, nil

	case RefSpace:
		return "", fmt.Errorf("%q points at a space, not a page. Try: sessh confluence space view %s", input, ref.Space)

	case RefTiny:
		// Only a redirect can tell us what a tiny link means; follow it and
		// re-parse where we landed.
		raw, err := session.Get(ctx, c.S, ref.Path)
		if err != nil {
			return "", err
		}
		if again := ParseRef(raw.URL); again != nil && again.Kind == RefID {
			return again.ID, nil
		}
		return "", fmt.Errorf("could not expand the tiny link %q (it redirected to %s)", input, raw.URL)
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
		return exact[0].ID, nil
	}
	hits, err := c.SearchText(ctx, title, space, "page", 5)
	if err != nil {
		return "", err
	}
	if len(hits) > 0 && hits[0].ID != "" {
		return hits[0].ID, nil
	}

	hint := ""
	if space == "" {
		hint = ` Try 'sessh confluence search "<text>"' to find it.`
	}
	return "", fmt.Errorf("no Confluence page found for %q.%s", input, hint)
}
