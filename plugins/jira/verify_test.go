package jira

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stefanm8/sessh/session"
)

type identitySession struct{ *requestSession }

func (identitySession) Navigate(context.Context, string) (string, error) {
	return "https://id.atlassian.com/login", nil
}

func (identitySession) Eval(_ context.Context, _ string, _, out any) error {
	*out.(*string) = "https://id.atlassian.com/login"
	return nil
}

func TestVerifyIdentity(t *testing.T) {
	t.Setenv("JIRA_SITE", "https://tenant.example")
	for _, status := range []int{200, 401, 403} {
		s := identitySession{&requestSession{respond: func(r *http.Request) (int, string, error) {
			if r.URL.Path != "/rest/api/3/myself" {
				t.Fatalf("identity request: %s", r.URL)
			}
			if status != 200 {
				return 0, "", &session.AuthError{Profile: "atlassian", Status: status}
			}
			return status, `{"accountId":"test-user","displayName":"Test User","emailAddress":"user@example.com"}`, nil
		}}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := verify(ctx, s)
		cancel()
		if status == 200 {
			if err != nil || len(s.requests) != 1 {
				t.Fatalf("valid identity: %v, requests=%d", err, len(s.requests))
			}
			id, err := whoami(context.Background(), s)
			if err != nil || id.AccountID != "test-user" || id.DisplayName != "Test User" || id.Email != "user@example.com" {
				t.Fatalf("identity=%+v, error=%v", id, err)
			}
		} else if !session.IsAuth(err) || !strings.Contains(err.Error(), "sessh auth login jira") || len(s.requests) != 2 {
			t.Fatalf("HTTP %d: error=%v, requests=%d", status, err, len(s.requests))
		}
	}
}
