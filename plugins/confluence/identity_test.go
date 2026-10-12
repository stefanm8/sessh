package confluence

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/session"
)

type identityResponse struct {
	session.Session
	status int
	body   string
}

func (s identityResponse) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader(s.body)), Request: req}, nil
}

func TestIdentityFailuresDoNotStartSSO(t *testing.T) {
	for _, response := range []identityResponse{
		{status: 500, body: `{"message":"unavailable"}`},
		{status: 429, body: `{"message":"rate limited"}`},
		{status: 200, body: `<html>upstream error</html>`},
		{status: 200, body: `{"accountId":42}`},
	} {
		// Navigate is deliberately absent: these are service failures, so
		// verification must return without visiting a login page.
		if err := verify(context.Background(), response); err == nil || session.IsAuth(err) {
			t.Fatalf("HTTP %d %s: got %v; want a service error", response.status, response.body, err)
		}
	}
}
