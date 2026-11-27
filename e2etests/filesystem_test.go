//go:build e2etests

package e2etests

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetupTestRepoClonesValidRepo(t *testing.T) {
	srcRepo := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.MkdirAll(srcRepo, 0o750))

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = srcRepo
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(output))
	}

	runGit("init")
	require.NoError(t, os.WriteFile(filepath.Join(srcRepo, "README.md"), []byte("hello\n"), 0o600))
	runGit("add", "README.md")
	runGit("-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "init")

	destRepo := filepath.Join(t.TempDir(), "dest")
	err := setupTestRepoWithRetry(destRepo, "file://"+srcRepo, 2, 10*time.Second, time.Millisecond)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(destRepo, "README.md"))
}

func TestSetupTestRepoReturnsErrorOnCloneFailure(t *testing.T) {
	destRepo := filepath.Join(t.TempDir(), "dest")
	err := setupTestRepoWithRetry(destRepo, filepath.Join(t.TempDir(), "nonexistent.git"), 2, 5*time.Second, time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "attempt 2/2")
}
