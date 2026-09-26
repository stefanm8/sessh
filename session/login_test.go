package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestLoginLock(t *testing.T) {
	isolatedHome(t)
	if LoggingIn("work") {
		t.Fatal("a login is reported before anyone started one")
	}
	release, waited, err := LockLogin(context.Background(), "work", nil)
	if err != nil || waited {
		t.Fatalf("first lock = %v, waited %v", err, waited)
	}
	if !LoggingIn("work") {
		t.Error("the held login lock is not reported")
	}
	if LoggingIn("other") {
		t.Error("a login for one profile is reported for another")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	told := 0
	if _, waited, err := LockLogin(ctx, "work", func() { told++ }); !errors.Is(err, context.DeadlineExceeded) || !waited || told != 1 {
		t.Fatalf("second lock = %v, waited %v, told %d; want it to wait and say so once", err, waited, told)
	}

	done := make(chan bool)
	go func() {
		release, waited, err := LockLogin(context.Background(), "work", nil)
		if err == nil {
			release()
		}
		done <- waited
	}()
	time.Sleep(100 * time.Millisecond)
	release()
	select {
	case waited := <-done:
		if !waited {
			t.Error("a login that had to wait does not report it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("releasing the login lock did not let the waiting login through")
	}
	if LoggingIn("work") {
		t.Error("a released login lock is still reported")
	}
}

// pageSession is a Session whose page sits on a login form.
type pageSession struct {
	Session
	navigated []string
}

func (p *pageSession) Navigate(_ context.Context, url string) (string, error) {
	p.navigated = append(p.navigated, url)
	return url, nil
}

func (p *pageSession) Eval(_ context.Context, js string, _, out any) error {
	b, _ := json.Marshal("https://idp.example/login")
	return json.Unmarshal(b, out)
}

func TestObserveDoesNotNavigate(t *testing.T) {
	page := &pageSession{}
	landed, err := Observe(page).Navigate(context.Background(), "https://tenant.example/app/")
	if err != nil || landed != "https://idp.example/login" {
		t.Fatalf("Navigate = %q, %v; want where the page already is", landed, err)
	}
	if len(page.navigated) != 0 {
		t.Fatalf("the page was moved to %v", page.navigated)
	}
}

// fakeHolder answers a client's requests the way a lock holder's socket does.
func fakeHolder(t *testing.T, conn net.Conn, answer func(rpcRequest) *rpcResponse) {
	t.Helper()
	go func() {
		defer conn.Close()
		dec := json.NewDecoder(bufio.NewReader(conn))
		for {
			var req rpcRequest
			if dec.Decode(&req) != nil {
				return
			}
			res := answer(req)
			res.ID = req.ID
			b, _ := json.Marshal(res)
			if _, err := conn.Write(append(b, '\n')); err != nil {
				return
			}
		}
	}()
}

func TestRoutedAuthFailureKeepsItsResponse(t *testing.T) {
	failure := &AuthError{Profile: "work", Detail: "not authenticated (HTTP 401)", Status: 401, URL: "https://tenant.example/rest/api/items"}
	holder := &Server{lastActivity: time.Now()}
	res := holder.dispatch(&rpcRequest{Method: "eval", Params: []byte(`{"js":"1"}`)}, func() (*browserSession, error) {
		return nil, failure
	})
	if !res.Auth || res.AuthStatus != 401 || res.AuthURL != failure.URL {
		t.Fatalf("holder response = %+v; want the status and URL of the refused request", res)
	}

	client, server := net.Pipe()
	fakeHolder(t, server, func(rpcRequest) *rpcResponse { return res })
	c := &rpcClient{conn: client, dec: json.NewDecoder(client), profile: Profile{Name: "work"}}
	defer c.Close()
	err := c.Eval(context.Background(), "1", nil, nil)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.URL != failure.URL || ae.Profile != "work" {
		t.Fatalf("client error = %#v; want the holder's AuthError", err)
	}
}

func TestRoutedRequestsUseTheCallersOrigin(t *testing.T) {
	client, server := net.Pipe()
	seen := map[string]string{}
	fakeHolder(t, server, func(req rpcRequest) *rpcResponse {
		switch req.Method {
		case "do":
			var p doParams
			_ = json.Unmarshal(req.Params, &p)
			seen["do"] = p.Path
			b, _ := json.Marshal(doResult{Status: 200, URL: p.Path, Body: []byte(`{}`)})
			return &rpcResponse{Result: b}
		case "navigate", "cookies":
			var u string
			_ = json.Unmarshal(req.Params, &u)
			seen[req.Method] = u
			if req.Method == "cookies" {
				return &rpcResponse{Result: []byte(`[]`)}
			}
			b, _ := json.Marshal(u)
			return &rpcResponse{Result: b}
		}
		return &rpcResponse{Error: "unexpected " + req.Method}
	})
	// The holder may serve another plugin's origin; relative URLs belong to
	// the plugin that made the request.
	c := &rpcClient{conn: client, dec: json.NewDecoder(client), profile: Profile{Name: "shared", Origin: "https://caller.example"}}
	defer c.Close()
	ctx := context.Background()
	req, _ := http.NewRequest(http.MethodGet, "/api/2.0/items", nil)
	resp, err := c.Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := c.Navigate(ctx, "/home"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cookies(ctx, "https://other.example/"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"do":       "https://caller.example/api/2.0/items",
		"navigate": "https://caller.example/home",
		"cookies":  "https://other.example/",
	}
	for method, url := range want {
		if seen[method] != url {
			t.Errorf("%s sent %q, want %q", method, seen[method], url)
		}
	}
}
