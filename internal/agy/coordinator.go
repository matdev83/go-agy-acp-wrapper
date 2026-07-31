package agy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const repoLockRetryInterval = 25 * time.Millisecond

// RepoCoordinator serializes AGY operations for the same working directory.
// Different directories use distinct cache entries and can run concurrently.
type RepoCoordinator struct {
	lockDir string
}

func NewRepoCoordinator(configDir string) *RepoCoordinator {
	return &RepoCoordinator{lockDir: filepath.Join(configDir, "wrapper-locks")}
}

func (c *RepoCoordinator) Lock(ctx context.Context, cwd string) (func() error, error) {
	canonicalCwd, err := canonicalWorkdir(cwd)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.lockDir, 0700); err != nil {
		return nil, fmt.Errorf("create wrapper lock dir: %w", err)
	}

	// Hash the canonical path to keep lock names portable and avoid exposing it.
	lockKey := canonicalCwd
	if runtime.GOOS == "windows" {
		lockKey = strings.ToLower(lockKey)
	}
	path := filepath.Join(c.lockDir, fmt.Sprintf("agy-workdir-%x.lock", sha256.Sum256([]byte(lockKey))))
	lock := flock.New(path, flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, repoLockRetryInterval)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock repository %q: %w", cwd, err)
	}
	if !locked {
		_ = lock.Close()
		return nil, ctx.Err()
	}
	return func() error {
		unlockErr := lock.Unlock()
		closeErr := lock.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}, nil
}

func canonicalWorkdir(cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("cwd is required")
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}
