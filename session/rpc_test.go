package session

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRPCCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			seen := make(chan rpcRequest, 1)
			go func() {
				var req rpcRequest
				_ = json.NewDecoder(server).Decode(&req)
				seen <- req
				if !deadline {
					cancel()
				}
			}()
			c := &rpcClient{conn: client, dec: json.NewDecoder(client)}
			start := time.Now()
			err := c.callContext(ctx, "do", nil, nil)
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("call = %v; want cancellation", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("cancellation did not interrupt socket read")
			}
			req := <-seen
			if req.Deadline == 0 || time.UnixMilli(req.Deadline).After(start.Add(66*time.Second)) {
				t.Fatalf("unbounded server request: %+v", req)
			}
		})
	}
}

func TestRPCRejectsMismatchedResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		var req rpcRequest
		_ = json.NewDecoder(server).Decode(&req)
		_ = json.NewEncoder(server).Encode(rpcResponse{ID: req.ID + 1, Result: json.RawMessage(`"wrong request"`)})
	}()
	c := &rpcClient{conn: client, dec: json.NewDecoder(client)}
	if err := c.call("ping", nil, nil); err == nil || !strings.Contains(err.Error(), "unexpected response ID") {
		t.Fatalf("call = %v; wanted to reject another call's response", err)
	}
}

func TestRPCQueuedCallCancellation(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan struct{})
	go func() {
		var req rpcRequest
		_ = json.NewDecoder(server).Decode(&req)
		close(seen)
	}()
	c := &rpcClient{conn: client, dec: json.NewDecoder(client)}
	done := make(chan error, 1)
	go func() { done <- c.callContext(ctx, "do", nil, nil) }()
	<-seen
	queued, end := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer end()
	if err := c.callContext(queued, "do", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued call = %v; wanted cancellation while the first call is still pending", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first call = %v; wanted cancellation", err)
	}
}

func TestDaemonActivity(t *testing.T) {
	s := &Server{lastActivity: time.Now().Add(-time.Hour)}
	s.dispatch(&rpcRequest{Method: "ping"}, nil)
	if s.IdleFor() < 59*time.Minute {
		t.Fatal("health check reset idle timeout")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.dispatch(&rpcRequest{Method: "eval", Params: []byte(`{"js":"1"}`)}, func() (*browserSession, error) {
			close(entered)
			<-release
			return nil, errors.New("test completed")
		})
	}()
	<-entered
	if s.IdleFor() != 0 {
		t.Error("in-flight request counted as idle")
	}
	close(release)
	<-done
	if s.IdleFor() > time.Second {
		t.Fatal("client work did not reset idle timeout")
	}
}
