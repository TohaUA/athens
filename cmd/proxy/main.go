package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"time"

	"github.com/gomods/athens/cmd/proxy/actions"
	"github.com/gomods/athens/internal/shutdown"
	"github.com/gomods/athens/pkg/build"
	"github.com/gomods/athens/pkg/config"
	athenslog "github.com/gomods/athens/pkg/log"
)

var (
	configFile    = flag.String("config_file", "", "The path to the config file")
	version       = flag.Bool("version", false, "Print version information and exit")
	verifyStorage = flag.Bool("verify-storage", false, "Verify stored module zips against the checksum database and report mismatches, then exit (does not start the server)")
	purge         = flag.Bool("purge", false, "With -verify-storage, delete the mismatched module versions (requires -verify-storage)")
)

// idleTimeout bounds how long an idle keep-alive connection is kept open.
//
// There is deliberately no ReadTimeout or WriteTimeout: Athens streams large
// module zips and serves long synchronous downloads, and those server-wide
// whole-request timeouts would cut them off.
const idleTimeout = 2 * time.Minute

// shutdowner is the part of *http.Server needed to shut it down.
type shutdowner interface {
	Shutdown(ctx context.Context) error
	Close() error
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       idleTimeout,
	}
}

// shutdownServer gracefully shuts srv down, giving in-flight requests up to
// timeout to finish. A failure (such as the timeout elapsing) is returned, and
// any connections still open are closed so that no handler is left running
// when the caller goes on to clean up.
func shutdownServer(srv shutdowner, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("could not shut down server: %w", err)
	}
	return nil
}

func exitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

// main only turns run's result into an exit status, so that the deferred
// cleanup in run always executes before the process exits.
func main() {
	os.Exit(run())
}

func run() int {
	flag.Parse()
	if err := actions.ValidateVerifyFlags(*verifyStorage, *purge); err != nil {
		stdlog.Fatalf("%v", err)
	}
	if *version {
		fmt.Println(build.String())
		os.Exit(0)
	}
	conf, err := config.Load(*configFile)
	if err != nil {
		stdlog.Fatalf("Could not load config file: %v", err)
	}

	logLvl, err := athenslog.ParseLevel(conf.LogLevel)
	if err != nil {
		stdlog.Fatalf("Could not parse log level %q: %v", conf.LogLevel, err)
	}

	logger := athenslog.New(conf.CloudRuntime, logLvl, conf.LogFormat)

	// Route the standard library logger's output through our logger at the
	// error level.
	stdlog.SetOutput(logger.StdLogger(slog.LevelError).Writer())
	stdlog.SetFlags(stdlog.Flags() &^ (stdlog.Ldate | stdlog.Ltime))

	// Athens shells out to the Go toolchain (go, git, ssh) and relies on an init
	// at PID 1 to reap the orphaned subprocesses they leave behind. Running as
	// PID 1 means there is no init to do that, so warn the operator.
	if os.Getpid() == 1 {
		logger.Warnf("Athens is running as PID 1 with no init to reap subprocesses; " +
			"run it under an init such as tini or `docker/podman run --init` to avoid zombie processes")
	}

	if *verifyStorage {
		if err := actions.RunVerify(conf, *purge, os.Stdout); err != nil {
			logger.Fatalf("verify-storage failed: %v", err)
		}
		return 0
	}

	handler, cleanup, err := actions.App(logger, conf)
	if err != nil {
		logger.Fatalf("Could not create App: %v", err)
	}
	defer cleanup()

	srv := newServer(handler)

	if conf.EnablePprof {
		go func() {
			// pprof to be exposed on a different port than the application for security matters,
			// not to expose profiling data and avoid DoS attacks (profiling slows down the service)
			// https://www.farsightsecurity.com/txt-record/2016/10/28/cmikk-go-remote-profiling/
			logger.WithFields(map[string]any{"port": conf.PprofPort}).Infof("starting pprof")
			logger.Fatalf("pprof server failed: %v", http.ListenAndServe(conf.PprofPort, nil)) //nolint:gosec // This should not be exposed to the world.
		}()
	}

	// Unix socket configuration, if available, takes precedence over TCP port configuration.
	var ln net.Listener

	if conf.UnixSocket != "" {
		logger.WithFields(map[string]any{"unixSocket": conf.UnixSocket}).Infof("Starting application")

		//nolint:noctx
		ln, err = net.Listen("unix", conf.UnixSocket)
		if err != nil {
			logger.Fatalf("Could not listen on Unix domain socket %q: %v", conf.UnixSocket, err)
		}
	} else {
		logger.WithFields(map[string]any{"tcpPort": conf.Port}).Infof("Starting application")

		//nolint:noctx
		ln, err = net.Listen("tcp", conf.Port)
		if err != nil {
			logger.Fatalf("Could not listen on TCP port %q: %v", conf.Port, err)
		}
	}

	signalCtx, signalStop := signal.NotifyContext(context.Background(), shutdown.GetSignals()...)

	go func() {
		defer signalStop()
		if conf.TLSCertFile != "" && conf.TLSKeyFile != "" {
			err = srv.ServeTLS(ln, conf.TLSCertFile, conf.TLSKeyFile)
		} else {
			err = srv.Serve(ln)
		}

		if !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("Could not start server: %v", err)
		}
	}()

	// Wait for shutdown signal, then cleanup before exit.
	<-signalCtx.Done()
	logger.Infof("Shutting down server")

	// We received an interrupt signal, shut down. On failure, log it and exit
	// non-zero; the deferred cleanup still runs before the process exits.
	shutdownErr := shutdownServer(srv, time.Second*time.Duration(conf.ShutdownTimeout))
	if shutdownErr != nil {
		logger.Errorf("%v", shutdownErr)
	}
	return exitCode(shutdownErr)
}
