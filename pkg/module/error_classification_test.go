package module

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/spf13/afero"
)

// writeFakeGo writes an executable shell script that impersonates the `go`
// binary so we can deterministically drive the subprocess output (stdout,
// stderr, exit code) that downloadModule / vcsLister.List classify.
//
// In production the flaky 500s are triggered by the PID-1 SIGCHLD reaper
// (internal/shutdown) winning the wait() race against os/exec, so cmd.Run
// returns a spurious "waitid: no child processes" and the exit code is lost.
// downloadModule / List now classify from the command's *output* rather than
// its exit code, so the bug reproduces deterministically through these fakes:
// each emits the exact stdout/stderr a real `go` produces for a given failure,
// with whatever exit code, and the classification must not change.
func writeFakeGo(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake go binary uses a POSIX shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "go")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	return bin
}

// realDir returns a real OS temp dir usable as cmd.Dir / GOPATH for the fake go.
func realDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestGoGetErrKindClassification proves that every go-tool failure signature
// observed in production maps deterministically to a kind the go client can
// recover from (404 or, for rate limiting, 429) — never a bare 500.
func TestGoGetErrKindClassification(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want int
	}{
		{"unknown revision", "go.uber.org/mock/mockgen@v0.6.0: invalid version: unknown revision mockgen/v0.6.0", errors.KindNotFound},
		{"no go-import meta tags", `go.uber.org@v0.6.0: unrecognized import path "go.uber.org": parse https://go.uber.org/?go-get=1: no go-import meta tags ()`, errors.KindNotFound},
		{"transient EOF", "EOF", errors.KindNotFound},
		{"opaque go failure", "exit status 1: go: some unexpected failure", errors.KindNotFound},
		{"github rate limit", "exit status 1: go: 403 response from api.github.com", errors.KindRateLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := goGetErrKind(tc.msg); got != tc.want {
				t.Fatalf("goGetErrKind(%q) = %d, want %d", tc.msg, got, tc.want)
			}
		})
	}
}

// TestDownloadModulePerModuleErrorIsNotFound reproduces the flaky 500 for
// `*.info` probes: the go subprocess writes a JSON object with an Error field
// to stdout, but a swallowed "no child processes" wait race makes cmd.Run()
// look successful, so downloadModule takes the `m.Error != ""` branch. Before
// the fix that branch set no kind and surfaced as KindUnexpected (500); it must
// be KindNotFound (404) so `go install pkg@version` path-walking can fall
// through to ,direct.
func TestDownloadModulePerModuleErrorIsNotFound(t *testing.T) {
	fakeGo := writeFakeGo(t, `#!/bin/sh
[ $# -eq 0 ] && exit 0
case "$1 $2" in
"mod download")
  printf '%s' '{"path":"go.uber.org/mock/mockgen","version":"v0.6.0","error":"go.uber.org/mock/mockgen@v0.6.0: invalid version: unknown revision mockgen/v0.6.0"}'
  exit 0 ;;
esac
exit 0
`)
	_, err := downloadModule(
		context.Background(), fakeGo, nil,
		realDir(t), realDir(t),
		"go.uber.org/mock/mockgen", "v0.6.0",
	)
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
	if got := errors.Kind(err); got != errors.KindNotFound {
		t.Fatalf("errors.Kind = %d (%s), want KindNotFound (404): %v", got, kindName(got), err)
	}
}

// TestDownloadModuleNoJSONFailureIsNotFound covers the failure branch where the
// subprocess exits non-zero and leaves no parseable JSON on stdout (the error
// went only to stderr). Before the fix this returned KindUnexpected (500).
func TestDownloadModuleNoJSONFailureIsNotFound(t *testing.T) {
	fakeGo := writeFakeGo(t, `#!/bin/sh
[ $# -eq 0 ] && exit 0
case "$1 $2" in
"mod download")
  echo 'go: go.uber.org@v0.6.0: unrecognized import path "go.uber.org": no go-import meta tags' 1>&2
  exit 1 ;;
esac
exit 0
`)
	_, err := downloadModule(
		context.Background(), fakeGo, nil,
		realDir(t), realDir(t),
		"go.uber.org", "v0.6.0",
	)
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
	if got := errors.Kind(err); got != errors.KindNotFound {
		t.Fatalf("errors.Kind = %d (%s), want KindNotFound (404): %v", got, kindName(got), err)
	}
}

// TestDownloadModuleConcurrentDeterministic proves the classification is
// deterministic under many concurrent identical fetches: every goroutine that
// hits the previously-500 branch must now return KindNotFound.
func TestDownloadModuleConcurrentDeterministic(t *testing.T) {
	fakeGo := writeFakeGo(t, `#!/bin/sh
[ $# -eq 0 ] && exit 0
case "$1 $2" in
"mod download")
  printf '%s' '{"path":"go.uber.org/mock/mockgen","version":"v0.6.0","error":"go.uber.org/mock/mockgen@v0.6.0: invalid version: unknown revision mockgen/v0.6.0"}'
  exit 0 ;;
esac
exit 0
`)
	const n = 24
	kinds := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := downloadModule(
				context.Background(), fakeGo, nil,
				realDir(t), realDir(t),
				"go.uber.org/mock/mockgen", "v0.6.0",
			)
			kinds[i] = errors.Kind(err)
		}(i)
	}
	wg.Wait()
	for i, k := range kinds {
		if k != errors.KindNotFound {
			t.Fatalf("fetch %d classified as %d (%s), want KindNotFound (404) for every concurrent fetch", i, k, kindName(k))
		}
	}
}

// TestVCSListerEOFIsNotFound reproduces the `*/@v/list` "EOF" 500: a swallowed
// "no child processes" wait race makes `go list` look successful while leaving
// stdout empty, so json.Decode returns io.EOF. Before the fix that surfaced as
// KindUnexpected (500); it must be KindNotFound (404).
func TestVCSListerEOFIsNotFound(t *testing.T) {
	fakeGo := writeFakeGo(t, `#!/bin/sh
[ $# -eq 0 ] && exit 0
exit 0
`)
	l := NewVCSLister(fakeGo, nil, afero.NewOsFs(), 30*time.Second)
	_, _, err := l.List(context.Background(), "golang.org")
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
	if got := errors.Kind(err); got != errors.KindNotFound {
		t.Fatalf("errors.Kind = %d (%s), want KindNotFound (404): %v", got, kindName(got), err)
	}
}

// http renders a kind/status int for readable failure messages.
func kindName(kind int) string {
	switch kind {
	case errors.KindNotFound:
		return "404 Not Found"
	case errors.KindUnexpected:
		return "500 Internal Server Error"
	case errors.KindRateLimit:
		return "429 Too Many Requests"
	case errors.KindGatewayTimeout:
		return "504 Gateway Timeout"
	default:
		return "status"
	}
}
