package session

import (
	"errors"
	"testing"
	"time"
)

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
