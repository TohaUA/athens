//go:build e2etests

package e2etests

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	cloneAttempts   = 3
	cloneTimeout    = 30 * time.Second
	cloneRetryDelay = 2 * time.Second
)

func setupTestRepo(repoPath, repoURL string) error {
	return setupTestRepoWithRetry(repoPath, repoURL, cloneAttempts, cloneTimeout, cloneRetryDelay)
}

func setupTestRepoWithRetry(repoPath, repoURL string, attempts int, timeout, retryDelay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := os.RemoveAll(repoPath); err != nil {
			return fmt.Errorf("failed to remove test repo dir %s: %w", repoPath, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, repoPath)
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}

		lastErr = fmt.Errorf("git clone %s (attempt %d/%d) failed: %w - %s", repoURL, attempt, attempts, err, strings.TrimSpace(string(output)))
		if attempt < attempts {
			time.Sleep(retryDelay)
		}
	}

	return lastErr
}

func chmodR(path string, mode os.FileMode) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	return fs.WalkDir(root.FS(), ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return root.Chmod(name, mode)
	})
}

func cleanGoCache(env []string) error {
	cmd := exec.CommandContext(context.Background(), "go", "clean", "--modcache")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to clear go cache: %w - %s", err, string(output))
	}
	return nil
}
