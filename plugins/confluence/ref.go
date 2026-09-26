package confluence

import (
	"net/url"
	"regexp"
	"strings"
)

// Page references.
//
// Anything you can copy out of Confluence should work as a page argument:
//
//	https://your-tenant.atlassian.net/wiki/spaces/ENG/pages/123456/Example+Page
//	/wiki/spaces/ENG/pages/123456
//	https://your-tenant.atlassian.net/wiki/x/AQAAAA            (tiny link)
//	https://your-tenant.atlassian.net/wiki/display/ENG/Some+Page
//	.../pages/viewpage.action?pageId=123456
//	123456                                          (bare id)
//	ENG:Some Page Title                                (space:title)
//	some page title                                     (falls back to search)
//
// ParseRef is pure. Turning a reference into an actual page id may need the API
// (title lookup) or a redirect (tiny link); that lives in ResolveRef.

// RefKind is what a reference turned out to be.
type RefKind string

const (
	RefID     RefKind = "id"
	RefTitle  RefKind = "title"
	RefTiny   RefKind = "tiny"
	RefSpace  RefKind = "space"
	RefSearch RefKind = "search"
)

// Ref is a parsed page reference.
type Ref struct {
	Kind  RefKind
	ID    string
	Space string
	Title string
	Text  string
	Path  string // tiny-link path
}

// spaceKey characters: alphanumerics plus ~ (personal spaces), _, - and .
const spaceKeyPattern = `[A-Za-z0-9~._-]+`

var (
	digitsRe    = regexp.MustCompile(`^\d+$`)
	pageIDQuery = regexp.MustCompile(`(?i)[?&]pageId=(\d+)`)
	spacePages  = regexp.MustCompile(`^/spaces/(` + spaceKeyPattern + `)/pages/(?:[a-z][a-z0-9-]*/)?(\d+)`)
	spaceBlog   = regexp.MustCompile(`^/spaces/(` + spaceKeyPattern + `)/blog/\d{4}/\d{1,2}/\d{1,2}/(\d+)`)
	barePages   = regexp.MustCompile(`^/pages/(\d+)`)
	displayRe   = regexp.MustCompile(`^/display/(` + spaceKeyPattern + `)/(.+)$`)
	tinyRe      = regexp.MustCompile(`^/x/([A-Za-z0-9_+/=~-]+)$`)
	spaceOnlyRe = regexp.MustCompile(`^/spaces/(` + spaceKeyPattern + `)(?:/(?:overview|pages|blog|all-updates))?$`)
	spaceTitle  = regexp.MustCompile(`^(` + spaceKeyPattern + `):\s*(\S.*)$`)
	trailingSl  = regexp.MustCompile(`/+$`)
)

// ParseRef classifies a reference without touching the network.
func ParseRef(input string) *Ref {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil
	}
	if digitsRe.MatchString(s) {
		return &Ref{Kind: RefID, ID: s}
	}

	var pathname, search string
	havePath := false
	if strings.HasPrefix(strings.ToLower(s), "http://") || strings.HasPrefix(strings.ToLower(s), "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return &Ref{Kind: RefSearch, Text: s}
		}
		pathname, search, havePath = u.Path, u.RawQuery, true
		if search != "" {
			search = "?" + search
		}
	} else if strings.HasPrefix(s, "/") {
		if q := strings.IndexByte(s, '?'); q == -1 {
			pathname = s
		} else {
			pathname, search = s[:q], s[q:]
		}
		havePath = true
	}

	if havePath {
		if ref := fromPath(decodePath(pathname), search); ref != nil {
			return ref
		}
		return &Ref{Kind: RefSearch, Text: s}
	}

	// SPACE:Title — a key never contains whitespace, which keeps titles that
	// merely contain a colon ("Example Guide: Naming") out of this branch.
	if m := spaceTitle.FindStringSubmatch(s); m != nil && !strings.ContainsAny(m[1], " \t\n") {
		return &Ref{Kind: RefTitle, Space: m[1], Title: strings.TrimSpace(m[2])}
	}
	return &Ref{Kind: RefSearch, Text: s}
}

func decodePath(p string) string {
	if d, err := url.PathUnescape(p); err == nil {
		return d
	}
	return p
}

func fromPath(pathname, search string) *Ref {
	// ?pageId=123 (viewpage.action and friends) wins wherever it appears.
	if m := pageIDQuery.FindStringSubmatch(search); m != nil {
		return &Ref{Kind: RefID, ID: m[1]}
	}

	// Drop the /wiki mount point so both site-absolute and _links.webui paths
	// work.
	p := pathname
	if p == "/wiki" {
		p = ""
	} else if strings.HasPrefix(p, "/wiki/") {
		p = p[len("/wiki"):]
	}
	p = trailingSl.ReplaceAllString(p, "")
	if p == "" {
		return nil
	}

	// /spaces/KEY/pages/ID[/Title] — also covers verbs like /pages/edit-v2/ID.
	// The optional verb must start with a letter, so a page whose *title*
	// begins with digits can't be mistaken for one.
	if m := spacePages.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefID, ID: m[2], Space: m[1]}
	}
	if m := spaceBlog.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefID, ID: m[2], Space: m[1]}
	}
	if m := barePages.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefID, ID: m[1]}
	}
	if m := displayRe.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefTitle, Space: m[1], Title: unslug(m[2])}
	}
	// /x/AbCdEf — a tiny link; only a redirect can tell us the real page.
	if m := tinyRe.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefTiny, Path: "/wiki/x/" + m[1]}
	}
	if m := spaceOnlyRe.FindStringSubmatch(p); m != nil {
		return &Ref{Kind: RefSpace, Space: m[1]}
	}
	return nil
}

// unslug turns a display-URL title back into text ('+' means space).
func unslug(s string) string {
	return strings.TrimSpace(decodePath(strings.ReplaceAll(s, "+", " ")))
}

// WebURL is the best-effort canonical web URL for a page-ish API object.
func WebURL(site string, links map[string]string, id, title, spaceKey string) string {
	site = strings.TrimRight(site, "/")
	wiki := site + "/wiki"
	if webui := links["webui"]; webui != "" {
		if strings.HasPrefix(webui, "/wiki") {
			return site + webui
		}
		return wiki + webui
	}
	if id != "" && spaceKey != "" {
		return wiki + "/spaces/" + url.PathEscape(spaceKey) + "/pages/" + id + "/" + displayPath(title)
	}
	if id != "" {
		return wiki + "/pages/viewpage.action?pageId=" + id
	}
	return wiki
}

// SpaceURL is a space's home.
func SpaceURL(site, key string) string {
	return strings.TrimRight(site, "/") + "/wiki/spaces/" + url.PathEscape(key)
}
