package session

import (
	"strings"
	"sync"
	"testing"
)

func TestStore_CreateAndGet(t *testing.T) {
	s := NewStore()
	ctx, err := s.Create("/tmp/workspace")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if ctx.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if !strings.HasPrefix(ctx.ID, "sess_") {
		t.Fatalf("expected session ID to start with sess_, got %q", ctx.ID)
	}
	if ctx.Cwd != "/tmp/workspace" {
		t.Fatalf("expected cwd /tmp/workspace, got %q", ctx.Cwd)
	}

	got, ok := s.Get(ctx.ID)
	if !ok {
		t.Fatal("expected Get to find session")
	}
	if got != ctx {
		t.Fatal("expected same context pointer")
	}
}

func TestStore_GetMissing(t *testing.T) {
	s := NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected Get to return false for missing session")
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewStore()
	ctx, _ := s.Create("/tmp/workspace")
	s.Delete(ctx.ID)

	_, ok := s.Get(ctx.ID)
	if ok {
		t.Fatal("expected session to be deleted")
	}
	if !ctx.IsClosed() {
		t.Fatal("expected session to be closed after delete")
	}
}

func TestStore_CloseAll(t *testing.T) {
	s := NewStore()
	c1, _ := s.Create("/tmp/a")
	c2, _ := s.Create("/tmp/b")

	s.CloseAll()

	if !c1.IsClosed() || !c2.IsClosed() {
		t.Fatal("expected all sessions to be closed")
	}
	_, ok1 := s.Get(c1.ID)
	_, ok2 := s.Get(c2.ID)
	if ok1 || ok2 {
		t.Fatal("expected all sessions removed from store")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, err := s.Create("/tmp/concurrent")
			if err != nil {
				t.Errorf("Create failed: %v", err)
				return
			}
			if _, ok := s.Get(ctx.ID); !ok {
				t.Errorf("Get failed for %s", ctx.ID)
			}
		}()
	}
	wg.Wait()
}
