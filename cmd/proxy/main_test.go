package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type fakeShutdowner struct {
	err      error
	deadline time.Time
	hasDL    bool
	closed   bool
}

func (f *fakeShutdowner) Shutdown(ctx context.Context) error {
	f.deadline, f.hasDL = ctx.Deadline()
	return f.err
}

func (f *fakeShutdowner) Close() error {
	f.closed = true
	return nil
}

func TestShutdownServerSuccess(t *testing.T) {
	f := &fakeShutdowner{}
	if err := shutdownServer(f, 5*time.Second); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if f.closed {
		t.Fatal("a clean shutdown must not force-close the server")
	}
	if !f.hasDL {
		t.Fatal("expected shutdown context to carry a deadline")
	}
	if remaining := time.Until(f.deadline); remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("deadline not derived from timeout, remaining=%v", remaining)
	}
}

func TestShutdownServerPropagatesError(t *testing.T) {
	want := context.DeadlineExceeded
	f := &fakeShutdowner{err: want}
	err := shutdownServer(f, time.Second)
	if err == nil {
		t.Fatal("expected shutdown failure to be returned, not swallowed")
	}
	if !f.closed {
		t.Fatal("expected the server to be force-closed when graceful shutdown fails")
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected error to wrap %v, got %v", want, err)
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Fatalf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("boom")); got != 1 {
		t.Fatalf("exitCode(err) = %d, want 1", got)
	}
}

func TestNewServerTimeouts(t *testing.T) {
	srv := newServer(http.NewServeMux())
	if srv.IdleTimeout != idleTimeout || idleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %v, want positive %v", srv.IdleTimeout, idleTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout must stay set")
	}
	// Athens streams large module zips and runs long synchronous downloads,
	// so whole-request read/write timeouts would break them.
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout/WriteTimeout must stay unset, got %v/%v", srv.ReadTimeout, srv.WriteTimeout)
	}
}
