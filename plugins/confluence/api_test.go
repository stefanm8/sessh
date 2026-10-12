package confluence

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/session"
)

type apiSession struct {
	session.Session
	t         *testing.T
	responses []apiResponse
	paths     []string
}

type apiResponse struct {
	status int
	body   string
}

func (s *apiSession) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	s.t.Helper()
	s.paths = append(s.paths, req.URL.String())
	if len(s.responses) == 0 {
		s.t.Fatalf("unexpected request: %s", req.URL)
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Request: req}, nil
}

func TestPagination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    pagination
		max     int
		bodies  []string
		queries []string
		count   int
	}{
		{"cursor is decoded and rebuilt on our endpoint", cursorPages, 0,
			[]string{`{"results":[1,2],"_links":{"next":"https://other.example/ignored?cursor=a%2Bb%26c"}}`, `{"results":[3]}`},
			[]string{"", "&cursor=a%2Bb%26c"}, 3},
		{"cursor caps results within a batch", cursorPages, 1,
			[]string{`{"results":[1,2],"_links":{"next":"?cursor=more"}}`}, []string{""}, 1},
		{"invalid cursor stops pagination", cursorPages, 0,
			[]string{`{"results":[1,2],"_links":{"next":"?cursor=%ZZ"}}`}, []string{""}, 2},
		{"empty cursor page stops even with a next link", cursorPages, 0,
			[]string{`{"results":[],"_links":{"next":"?cursor=more"}}`}, []string{""}, 0},
		{"offset advances by received results", offsetPages, 0,
			[]string{`{"results":[1,2]}`, `{"results":[3]}`}, []string{"&start=0", "&start=2"}, 3},
		{"offset honors total size", offsetPages, 0,
			[]string{`{"results":[1,2],"totalSize":2}`}, []string{"&start=0"}, 2},
		{"offset honors reported batch size", offsetPages, 0,
			[]string{`{"results":[1,2],"size":1}`}, []string{"&start=0"}, 2},
		{"search ignores content batch size", searchPages, 0,
			[]string{`{"results":[1,2],"size":1}`, `{"results":[3]}`}, []string{"&start=0", "&start=2"}, 3},
		{"offset caps results across batches", offsetPages, 3,
			[]string{`{"results":[1,2]}`, `{"results":[3,4]}`}, []string{"&start=0", "&start=2"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &apiSession{t: t}
			for _, body := range tc.bodies {
				s.responses = append(s.responses, apiResponse{200, body})
			}
			rows, supported, err := New(s).paginate(context.Background(), "/items?filter=x", tc.max, 2, tc.mode)
			if err != nil || !supported || len(rows) != tc.count || len(s.responses) != 0 {
				t.Fatalf("rows=%s supported=%v error=%v unused responses=%d", rows, supported, err, len(s.responses))
			}
			for i, query := range tc.queries {
				if want := "/items?filter=x&limit=2" + query; s.paths[i] != want {
					t.Errorf("request %d = %q, want %q", i, s.paths[i], want)
				}
			}
		})
	}
}

func TestListFallback(t *testing.T) {
	for _, status := range []int{404, 405, 410} {
		s := &apiSession{t: t, responses: []apiResponse{
			{400, `{"message":"unsupported sort"}`},
			{status, "unavailable"},
			{200, `{"results":[{"id":123,"title":"Legacy page"}]}`},
		}}
		rows, err := New(s).list(context.Background(), "/v2?filter=x", "sort=name", "/v1?expand=space", 5, 2)
		want := []string{"/v2?filter=x&sort=name&limit=2", "/v2?filter=x&limit=2", "/v1?expand=space&limit=2&start=0"}
		if err != nil || len(rows) != 1 || !reflect.DeepEqual(s.paths, want) {
			t.Fatalf("status=%d rows=%s requests=%v error=%v", status, rows, s.paths, err)
		}
	}
	for _, response := range []apiResponse{{403, `{"message":"forbidden"}`}, {500, `{"message":"failed"}`}, {200, `{"results":42}`}} {
		s := &apiSession{t: t, responses: []apiResponse{response}}
		if _, err := New(s).list(context.Background(), "/v2", "sort=name", "/v1", 5, 2); err == nil {
			t.Fatalf("expected failure for %+v", response)
		}
	}
}

func TestGetPageRenderedFallback(t *testing.T) {
	s := &apiSession{t: t, responses: []apiResponse{
		{404, "unavailable"},
		{200, `{"id":123,"title":"Page","space":{"key":"ENG"},"body":{"storage":{"value":" "}}}`},
		{200, `{"body":{"view":{"value":"<p>Rendered</p>"}}}`},
	}}
	p, err := New(s).GetPage(context.Background(), "123", "storage")
	if err != nil || p.ID != "123" || p.SpaceKey != "ENG" || p.Body != "<p>Rendered</p>" || p.BodyFormat != "view" {
		t.Fatalf("page=%+v error=%v", p, err)
	}
}
