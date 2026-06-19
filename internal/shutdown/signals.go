//go:build unix

package shutdown

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
)

// GetSignals returns the appropriate signals to catch for a clean shutdown, dependent on the OS.
//
// On Unix-like operating systems, it is important to catch SIGTERM in addition to SIGINT.
func GetSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// ChildProcReaper spawns a goroutine to listen for SIGCHLD signals to cleanup
// zombie child processes. The returned context will be canceled once all child
// processes have been cleaned up, and should be waited on before exiting.
//
// This only applies to Unix platforms, and returns an already canceled context
// on Windows.
func ChildProcReaper(ctx context.Context, logger logrus.FieldLogger) context.Context {
	done, cancel := context.WithCancel(context.WithoutCancel(ctx))

	// Reaping reparented orphans is only meaningful when this process is the
	// one orphans reparent to — i.e. PID 1. We do not call
	// PR_SET_CHILD_SUBREAPER, so when Athens is not PID 1 (running under tini,
	// dumb-init, a shared-process-namespace pod, systemd, or in tests) a real
	// init reaps orphans for us. A greedy Wait4(-1) reaper here would then only
	// race os/exec for our own `go` subprocesses: the reaper waits the process
	// first, os/exec's wait returns "waitid: no child processes" (ECHILD), and
	// the go command's exit status is lost — the source of flaky 500s on module
	// path-walk probes. So only install the reaper when we are PID 1.
	if os.Getpid() != 1 {
		cancel()
		return done
	}

	sigChld := make(chan os.Signal, 1)
	signal.Notify(sigChld, syscall.SIGCHLD)

	go func() {
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				reap(logger)
				return
			case <-sigChld:
				reap(logger)
			}
		}
	}()

	return done
}

func reap(logger logrus.FieldLogger) {
	for {
		var wstatus syscall.WaitStatus

		pid, err := syscall.Wait4(-1, &wstatus, syscall.WNOHANG, nil)
		if err != nil && !errors.Is(err, syscall.ECHILD) {
			logger.Errorf("failed to reap child process: %v", err)
			continue
		} else if pid <= 0 {
			return
		}

		logger.Infof("reaped child process %v, exit status: %v", pid, wstatus.ExitStatus())
	}
}
