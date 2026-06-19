//go:build unix

package shutdown

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// TestChildProcReaperSkippedWhenNotPID1 verifies that the SIGCHLD reaper is not
// installed unless this process is PID 1. Reaping reparented orphans is only
// meaningful at PID 1 (we never call PR_SET_CHILD_SUBREAPER); installing a
// greedy Wait4(-1) reaper otherwise only races os/exec for our own `go`
// subprocesses, producing the spurious ECHILD that loses their exit status.
//
// The test process is not PID 1, so the returned context must already be done.
func TestChildProcReaperSkippedWhenNotPID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("test process is PID 1; cannot exercise the non-PID-1 path")
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	done := ChildProcReaper(context.Background(), logger)

	select {
	case <-done.Done():
		// expected: no reaper goroutine was started
	case <-time.After(2 * time.Second):
		t.Fatal("expected ChildProcReaper to be a no-op (already done) when not PID 1")
	}
}
