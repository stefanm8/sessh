package confluence

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stefanm8/sessh/session"
)

type identityResponse struct {
	session.Session
	status int
	body   string
}

func (s identityResponse) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	if s.status == 401 || s.status == 403 {
		return nil, &session.AuthError{Profile: "atlassian", Status: s.status}
	}
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader(s.body)), Request: req}, nil
}

func TestIdentityFailuresDoNotStartSSO(t *testing.T) {
	for _, response := range []identityResponse{
		{status: 500, body: `{"message":"unavailable"}`},
		{status: 429, body: `{"message":"rate limited"}`},
		{status: 200, body: `<html>upstream error</html>`},
		{status: 200, body: `{"accountId":42}`},
	} {
		// Service failures must retain their classification without SSO retries.
		if err := verify(context.Background(), response); err == nil || session.IsAuth(err) {
			t.Fatalf("HTTP %d %s: got %v; want a service error", response.status, response.body, err)
		}
	}
}

func (identityResponse) Navigate(context.Context, string) (string, error) {
	return "https://id.atlassian.com/login", nil
}

func (identityResponse) Eval(_ context.Context, _ string, _, out any) error {
	*out.(*string) = "https://id.atlassian.com/login"
	return nil
}

func TestVerifyIdentity(t *testing.T) {
	t.Setenv("CONFLUENCE_SITE", "https://tenant.example")
	for _, response := range []identityResponse{
		{status: 200, body: `{"accountId":"test-user","displayName":"Test User","email":"user@example.com"}`},
		{status: 401}, {status: 403},
		{status: 200, body: `{"type":"anonymous","displayName":"Anonymous"}`},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := verify(ctx, response)
		cancel()
		if strings.Contains(response.body, "accountId") {
			if err != nil {
				t.Fatal(err)
			}
			id, err := whoami(context.Background(), response)
			if err != nil || id.AccountID != "test-user" || id.DisplayName != "Test User" || id.Email != "user@example.com" {
				t.Fatalf("identity=%+v, error=%v", id, err)
			}
		} else if !session.IsAuth(err) || !strings.Contains(err.Error(), "sessh auth login confluence") {
			t.Fatalf("HTTP %d %s: got %v; want Confluence login hint", response.status, response.body, err)
		}
	}
}
