package agy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRepoCoordinator_SerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("GO_WANT_REPO_LOCK_HELPER") == "1" {
		configDir := os.Getenv("REPO_LOCK_CONFIG_DIR")
		cwd := os.Getenv("REPO_LOCK_CWD")
		coordinator := NewRepoCoordinator(configDir)
		unlock, err := coordinator.Lock(context.Background(), cwd)
		if err != nil {
			os.Exit(2)
		}
		defer unlock()
		if err := os.WriteFile(os.Getenv("REPO_LOCK_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(300 * time.Millisecond)
		return
	}

	configDir := t.TempDir()
	cwd := t.TempDir()
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepoCoordinator_SerializesAcrossProcesses$")
	cmd.Env = append(os.Environ(),
		"GO_WANT_REPO_LOCK_HELPER=1",
		"REPO_LOCK_CONFIG_DIR="+configDir,
		"REPO_LOCK_CWD="+cwd,
		"REPO_LOCK_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire repository lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	coordinator := NewRepoCoordinator(configDir)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if unlock, err := coordinator.Lock(ctx, cwd); err == nil {
		_ = unlock()
		t.Fatal("second process unexpectedly acquired the same repository lock")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock holder failed: %v", err)
	}

	unlock, err := coordinator.Lock(context.Background(), cwd)
	if err != nil {
		t.Fatalf("lock remained held after helper exited: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

func TestRepoCoordinator_DifferentReposRunAcrossProcesses(t *testing.T) {
	if os.Getenv("GO_WANT_DIFFERENT_REPO_LOCK_HELPER") == "1" {
		coordinator := NewRepoCoordinator(os.Getenv("REPO_LOCK_CONFIG_DIR"))
		unlock, err := coordinator.Lock(context.Background(), os.Getenv("REPO_LOCK_CWD"))
		if err != nil {
			os.Exit(2)
		}
		defer unlock()
		if err := os.WriteFile(os.Getenv("REPO_LOCK_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(300 * time.Millisecond)
		return
	}

	configDir := t.TempDir()
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepoCoordinator_DifferentReposRunAcrossProcesses$")
	cmd.Env = append(os.Environ(),
		"GO_WANT_DIFFERENT_REPO_LOCK_HELPER=1",
		"REPO_LOCK_CONFIG_DIR="+configDir,
		"REPO_LOCK_CWD="+t.TempDir(),
		"REPO_LOCK_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire first repository lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	coordinator := NewRepoCoordinator(configDir)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	unlock, err := coordinator.Lock(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("different repository was blocked by another wrapper process: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock second repository: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock holder failed: %v", err)
	}
}

func TestRepoCoordinator_SameRepoSerializesAcrossInstances(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	first := NewRepoCoordinator(configDir)
	second := NewRepoCoordinator(configDir)
	unlockFirst, err := first.Lock(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if unlockSecond, err := second.Lock(ctx, cwd); err == nil {
		_ = unlockSecond()
		t.Fatal("second wrapper instance unexpectedly acquired the same repository lock")
	}
}
