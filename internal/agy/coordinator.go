package agy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const repoLockRetryInterval = 25 * time.Millisecond

// RepoCoordinator serializes AGY operations that share the global AGY cache.
// AGY records conversations in one last_conversations.json file, so even
// different repositories must coordinate unless AGY provides an invocation ID.
type RepoCoordinator struct {
	lockDir string
}

func NewRepoCoordinator(configDir string) *RepoCoordinator {
	return &RepoCoordinator{lockDir: filepath.Join(configDir, "wrapper-locks")}
}

func (c *RepoCoordinator) Lock(ctx context.Context, cwd string) (func() error, error) {
	if _, err := canonicalWorkdir(cwd); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.lockDir, 0700); err != nil {
		return nil, fmt.Errorf("create wrapper lock dir: %w", err)
	}

	path := filepath.Join(c.lockDir, "agy-global.lock")
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
