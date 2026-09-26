package confluence

import (
	"testing"
)

const site = "https://tenant.example"

func TestParseRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want *Ref
	}{
		// --- URLs you can copy out of the browser ---------------------------
		{
			name: "a full page URL yields the id and the space",
			in:   site + "/wiki/spaces/ENG/pages/123456/Example+Project+Guide",
			want: &Ref{Kind: RefID, ID: "123456", Space: "ENG"},
		},
		{
			name: "a page URL without a title slug still resolves",
			in:   site + "/wiki/spaces/ENG/pages/123456",
			want: &Ref{Kind: RefID, ID: "123456", Space: "ENG"},
		},
		{
			name: "a trailing slash is ignored",
			in:   site + "/wiki/spaces/ENG/pages/123456/",
			want: &Ref{Kind: RefID, ID: "123456", Space: "ENG"},
		},
		{
			name: "a fragment does not defeat the match",
			in:   site + "/wiki/spaces/ENG/pages/123/Title#Some-Heading",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "a query string does not defeat the match",
			in:   site + "/wiki/spaces/ENG/pages/123/Title?focusedCommentId=9",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "an http URL works the same as https",
			in:   "http://tenant.example/wiki/spaces/ENG/pages/123",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "the scheme is matched case-insensitively",
			in:   "HTTPS://tenant.example/wiki/spaces/ENG/pages/123",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "an edit URL carries a verb before the id",
			in:   site + "/wiki/spaces/ENG/pages/edit-v2/777",
			want: &Ref{Kind: RefID, ID: "777", Space: "ENG"},
		},
		{
			name: "a blog post URL yields the id",
			in:   site + "/wiki/spaces/ENG/blog/2026/07/30/999/Post",
			want: &Ref{Kind: RefID, ID: "999", Space: "ENG"},
		},
		{
			name: "a personal space key with a tilde is accepted",
			in:   site + "/wiki/spaces/~example123/pages/123/Notes",
			want: &Ref{Kind: RefID, ID: "123", Space: "~example123"},
		},

		// --- /wiki-prefixed and bare paths ----------------------------------
		{
			name: "the site-relative webui path returned by the API",
			in:   "/spaces/ENG/pages/123/Title",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "the same path with the /wiki mount point",
			in:   "/wiki/spaces/ENG/pages/123/Title",
			want: &Ref{Kind: RefID, ID: "123", Space: "ENG"},
		},
		{
			name: "a bare /pages/<id> path with no space",
			in:   "/wiki/pages/123",
			want: &Ref{Kind: RefID, ID: "123"},
		},

		// --- pageId= queries -------------------------------------------------
		{
			name: "viewpage.action?pageId",
			in:   site + "/wiki/pages/viewpage.action?pageId=555",
			want: &Ref{Kind: RefID, ID: "555"},
		},
		{
			name: "pageId later in the query string",
			in:   site + "/wiki/pages/viewpage.action?spaceKey=ENG&pageId=555",
			want: &Ref{Kind: RefID, ID: "555"},
		},
		{
			name: "pageId is matched case-insensitively",
			in:   site + "/wiki/pages/viewpage.action?PAGEID=555",
			want: &Ref{Kind: RefID, ID: "555"},
		},
		{
			name: "pageId wins over whatever the path says",
			in:   site + "/wiki/spaces/ENG/pages/111/Title?pageId=555",
			want: &Ref{Kind: RefID, ID: "555"},
		},
		{
			name: "a relative path with a pageId query",
			in:   "/wiki/pages/viewpage.action?pageId=555",
			want: &Ref{Kind: RefID, ID: "555"},
		},

		// --- display URLs -----------------------------------------------------
		{
			name: "a display URL yields the space and the title",
			in:   site + "/wiki/display/ENG/Example+Guide",
			want: &Ref{Kind: RefTitle, Space: "ENG", Title: "Example Guide"},
		},
		{
			name: "a percent-encoded display title is decoded",
			in:   site + "/wiki/display/ENG/Example%20Guide",
			want: &Ref{Kind: RefTitle, Space: "ENG", Title: "Example Guide"},
		},
		{
			name: "a display title beginning with digits stays a title",
			in:   site + "/wiki/display/ENG/2026+Roadmap",
			want: &Ref{Kind: RefTitle, Space: "ENG", Title: "2026 Roadmap"},
		},

		// --- tiny links -------------------------------------------------------
		{
			name: "a tiny link cannot be resolved offline",
			in:   site + "/wiki/x/AQAAAA",
			want: &Ref{Kind: RefTiny, Path: "/wiki/x/AQAAAA"},
		},
		{
			name: "a tiny link path without the host",
			in:   "/wiki/x/AQAAAA",
			want: &Ref{Kind: RefTiny, Path: "/wiki/x/AQAAAA"},
		},

		// --- spaces -----------------------------------------------------------
		{
			name: "a space URL",
			in:   site + "/wiki/spaces/ENG",
			want: &Ref{Kind: RefSpace, Space: "ENG"},
		},
		{
			name: "a space overview URL",
			in:   site + "/wiki/spaces/ENG/overview",
			want: &Ref{Kind: RefSpace, Space: "ENG"},
		},
		{
			name: "a space pages index",
			in:   site + "/wiki/spaces/ENG/pages",
			want: &Ref{Kind: RefSpace, Space: "ENG"},
		},

		// --- bare ids and SPACE:Title ----------------------------------------
		{
			name: "a bare id",
			in:   "123456",
			want: &Ref{Kind: RefID, ID: "123456"},
		},
		{
			name: "a bare id with surrounding whitespace",
			in:   "  123456  ",
			want: &Ref{Kind: RefID, ID: "123456"},
		},
		{
			name: "SPACE:Title",
			in:   "ENG:Example Page",
			want: &Ref{Kind: RefTitle, Space: "ENG", Title: "Example Page"},
		},
		{
			name: "SPACE: Title tolerates the space after the colon",
			in:   "ENG: Example Page",
			want: &Ref{Kind: RefTitle, Space: "ENG", Title: "Example Page"},
		},
		{
			name: "a personal space key in SPACE:Title form",
			in:   "~example123:My Notes",
			want: &Ref{Kind: RefTitle, Space: "~example123", Title: "My Notes"},
		},

		// --- deliberate near-misses -------------------------------------------
		{
			name: "a title that merely contains a colon is free text, not SPACE:Title",
			in:   "Project Guide: Getting Started",
			want: &Ref{Kind: RefSearch, Text: "Project Guide: Getting Started"},
		},
		{
			name: "a colon with nothing after it is free text",
			in:   "ENG:",
			want: &Ref{Kind: RefSearch, Text: "ENG:"},
		},
		{
			name: "a colon followed only by whitespace is free text",
			in:   "ENG:   ",
			want: &Ref{Kind: RefSearch, Text: "ENG:"},
		},
		{
			name: "an id with a suffix is not an id",
			in:   "123456x",
			want: &Ref{Kind: RefSearch, Text: "123456x"},
		},

		// --- free text and empties --------------------------------------------
		{
			name: "free text falls back to search",
			in:   "example project guide",
			want: &Ref{Kind: RefSearch, Text: "example project guide"},
		},
		{
			name: "an unrecognised URL falls back to search on the whole string",
			in:   site + "/wiki/somewhere/else",
			want: &Ref{Kind: RefSearch, Text: site + "/wiki/somewhere/else"},
		},
		{
			name: "the wiki root alone is not a reference",
			in:   site + "/wiki",
			want: &Ref{Kind: RefSearch, Text: site + "/wiki"},
		},
		{
			name: "an unparseable URL degrades to search rather than erroring",
			in:   "https://[::1",
			want: &Ref{Kind: RefSearch, Text: "https://[::1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRef(tt.in)
			if got == nil {
				t.Fatalf("ParseRef(%q) = nil, want %+v", tt.in, tt.want)
			}
			if *got != *tt.want {
				t.Errorf("ParseRef(%q) =\n  %+v\nwant\n  %+v", tt.in, *got, *tt.want)
			}
		})
	}
}

func TestParseRefEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"an empty string is not a reference", ""},
		{"whitespace only is not a reference", "   \t\n "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseRef(tt.in); got != nil {
				t.Errorf("ParseRef(%q) = %+v, want nil", tt.in, got)
			}
		})
	}
}

func TestWebURL(t *testing.T) {
	tests := []struct {
		name     string
		site     string
		links    map[string]string
		id       string
		title    string
		spaceKey string
		want     string
	}{
		{
			name:  "the API's webui link is preferred and gets the /wiki mount point",
			site:  site,
			links: map[string]string{"webui": "/spaces/ENG/pages/1/T"},
			id:    "1", title: "T", spaceKey: "ENG",
			want: site + "/wiki/spaces/ENG/pages/1/T",
		},
		{
			name:  "a webui link that already carries /wiki is not doubled",
			site:  site,
			links: map[string]string{"webui": "/wiki/spaces/ENG/pages/1/T"},
			want:  site + "/wiki/spaces/ENG/pages/1/T",
		},
		{
			name: "without a link, space and id build a display path",
			site: site,
			id:   "9", title: "Hello World", spaceKey: "ENG",
			want: site + "/wiki/spaces/ENG/pages/9/Hello+World",
		},
		{
			name: "a title with a slash is escaped in the path",
			site: site,
			id:   "9", title: "A/B", spaceKey: "ENG",
			want: site + "/wiki/spaces/ENG/pages/9/A%2FB",
		},
		{
			name: "an id with no space falls back to viewpage.action",
			site: site,
			id:   "9", title: "Hello",
			want: site + "/wiki/pages/viewpage.action?pageId=9",
		},
		{
			name: "with nothing to go on we still return the wiki root",
			site: site,
			want: site + "/wiki",
		},
		{
			name: "a trailing slash on the site is trimmed",
			site: site + "/",
			id:   "9", spaceKey: "ENG", title: "T",
			want: site + "/wiki/spaces/ENG/pages/9/T",
		},
		{
			name:  "an empty webui link is ignored",
			site:  site,
			links: map[string]string{"webui": ""},
			id:    "9", spaceKey: "ENG", title: "T",
			want: site + "/wiki/spaces/ENG/pages/9/T",
		},
		{
			name: "a personal space key survives escaping",
			site: site,
			id:   "9", spaceKey: "~example123", title: "T",
			want: site + "/wiki/spaces/~example123/pages/9/T",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WebURL(tt.site, tt.links, tt.id, tt.title, tt.spaceKey)
			if got != tt.want {
				t.Errorf("WebURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpaceURL(t *testing.T) {
	tests := []struct {
		name string
		site string
		key  string
		want string
	}{
		{"a plain key", site, "ENG", site + "/wiki/spaces/ENG"},
		{"a trailing slash on the site is trimmed", site + "/", "ENG", site + "/wiki/spaces/ENG"},
		{"a personal space key", site, "~example123", site + "/wiki/spaces/~example123"},
		{"an empty key still yields a path", site, "", site + "/wiki/spaces/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SpaceURL(tt.site, tt.key); got != tt.want {
				t.Errorf("SpaceURL(%q, %q) = %q, want %q", tt.site, tt.key, got, tt.want)
			}
		})
	}
}

func TestUnslug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plus signs become spaces", "Example+Guide", "Example Guide"},
		{"percent escapes are decoded", "Example%20Guide", "Example Guide"},
		{"a mix of both", "A+B%2DC", "A B-C"},
		{"surrounding whitespace is trimmed", "+A+", "A"},
		{"an undecodable escape leaves the string as-is rather than dropping it", "100%+done", "100% done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unslug(tt.in); got != tt.want {
				t.Errorf("unslug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
