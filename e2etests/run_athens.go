//go:build e2etests

package e2etests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"path/filepath"
	"time"
)

func buildAthens(goBin, destPath string, env []string) (string, error) {
	target := path.Join(destPath, "athens-proxy")
	binFolder, err := filepath.Abs("../cmd/proxy")
	if err != nil {
		return "", fmt.Errorf("failed to get athens source path: %w", err)
	}

	cmd := exec.CommandContext(context.Background(), goBin, "build", "-o", target, binFolder)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to build athens: %w - %s", err, string(output))
	}
	return target, nil
}

func stopAthens() error {
	cmd := exec.CommandContext(context.Background(), "pkill", "athens-proxy")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop athens: %w - %s", err, string(output))
	}
	return nil
}

func runAthensAndWait(ctx context.Context, athensBin string, env []string) error {
	cmd := exec.CommandContext(ctx, athensBin)
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start athens: %w", err)
	}

	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("athens exited before becoming ready: %w", err)
			}
			return errors.New("athens exited before becoming ready")
		case <-ctx.Done():
			return fmt.Errorf("context canceled waiting for athens: %w", ctx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:3000/readyz", nil)
			if err != nil {
				return fmt.Errorf("failed to build readiness request: %w", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		case <-timeout.C:
			return errors.New("failed to run athens: timed out waiting for readyz")
		}
	}
}
