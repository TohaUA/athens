package stash

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// TestSingleFlight will ensure that 5 concurrent requests will all get the first request's
// response. We can ensure that because only the first response does not return an error
// and therefore all 5 responses should have no error.
func TestSingleFlight(t *testing.T) {
	ms := &mockSFStasher{}
	s := WithSingleflight(ms)

	var eg errgroup.Group
	for range 5 {
		eg.Go(func() error {
			_, err := s.Stash(t.Context(), "mod", "ver")
			return err
		})
	}

	err := eg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	for range 5 {
		eg.Go(func() error {
			_, err := s.Stash(t.Context(), "mod", "ver")
			return err
		})
	}
	err = eg.Wait()
	if err == nil {
		t.Fatal("expected second error to return")
	}
}

// mockSFStasher mocks a Stash request that
// will always return a different result after the
// first one. This way we can prove that a second
// request did not get a second result, but the first
// one, provided the request came in at the right time.
type mockSFStasher struct {
	mu  sync.Mutex
	num int
}

func (ms *mockSFStasher) Stash(ctx context.Context, mod, ver string) (string, error) {
	time.Sleep(time.Millisecond * 100) // allow for second requests to come in.
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.num == 0 {
		ms.num++
		return "", nil
	}
	return "", fmt.Errorf("second time error")
}

// gatedSFStasher blocks Stash until release is closed and then returns a fixed result.
// It records whether the context it was given was already done when it finished.
type gatedSFStasher struct {
	started chan struct{}
	release chan struct{}
	ctxDone chan bool
}

func newGatedSFStasher() *gatedSFStasher {
	return &gatedSFStasher{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		ctxDone: make(chan bool, 1),
	}
}

func (g *gatedSFStasher) Stash(ctx context.Context, mod, ver string) (string, error) {
	g.started <- struct{}{}
	<-g.release
	g.ctxDone <- ctx.Err() != nil
	return "real", nil
}

type sfResult struct {
	ver string
	err error
}

func sfAsync(ctx context.Context, s Stasher) <-chan sfResult {
	ch := make(chan sfResult, 1)
	go func() {
		v, err := s.Stash(ctx, "mod", "ver")
		ch <- sfResult{v, err}
	}()
	return ch
}

// deadlineSFStasher records the deadline of the context it is given.
type deadlineSFStasher struct {
	deadline chan *time.Time
}

func (d *deadlineSFStasher) Stash(ctx context.Context, mod, ver string) (string, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.deadline <- &dl
	} else {
		d.deadline <- nil
	}
	return "real", nil
}

// TestSingleFlightKeepsDeadline ensures the shared work is detached from the
// first waiter's cancellation but stays bounded by that waiter's deadline.
func TestSingleFlightKeepsDeadline(t *testing.T) {
	want := time.Now().Add(time.Hour)

	d := &deadlineSFStasher{deadline: make(chan *time.Time, 1)}
	ctx, cancel := context.WithDeadline(t.Context(), want)
	defer cancel()
	if _, err := WithSingleflight(d).Stash(ctx, "mod", "ver"); err != nil {
		t.Fatal(err)
	}
	got := <-d.deadline
	if got == nil || !got.Equal(want) {
		t.Fatalf("shared work context deadline = %v, want %v", got, want)
	}

	// Without a caller deadline the work context must not invent one.
	d = &deadlineSFStasher{deadline: make(chan *time.Time, 1)}
	if _, err := WithSingleflight(d).Stash(t.Context(), "mod", "ver"); err != nil {
		t.Fatal(err)
	}
	if got := <-d.deadline; got != nil {
		t.Fatalf("expected no deadline, got %v", got)
	}
}

// TestSingleFlightWaiterCancel ensures a waiter whose context is cancelled returns
// promptly while another waiter on the same module still gets the real result.
func TestSingleFlightWaiterCancel(t *testing.T) {
	g := newGatedSFStasher()
	s := WithSingleflight(g)

	// The cancelled waiter is the first one, so it is also the one that starts the work.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelled := sfAsync(ctx, s)
	<-g.started
	other := sfAsync(t.Context(), s)
	time.Sleep(50 * time.Millisecond) // let the second waiter subscribe.
	cancel()

	select {
	case r := <-cancelled:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("expected context canceled error, got %v", r.err)
		}
	case <-time.After(2 * time.Second):
		close(g.release)
		t.Fatal("cancelled waiter did not return promptly")
	}

	close(g.release)
	select {
	case r := <-other:
		if r.err != nil || r.ver != "real" {
			t.Fatalf("other waiter got unexpected result: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("other waiter did not receive the result")
	}

	if done := <-g.ctxDone; done {
		t.Fatal("shared stash work context was cancelled by a departing waiter")
	}

	// The in-flight entry must be cleaned up.
	impl, ok := s.(*withsf)
	if !ok {
		t.Fatalf("unexpected stasher type %T", s)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		impl.mu.Lock()
		n := len(impl.subs)
		impl.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected subs map to be empty, has %d entries", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
