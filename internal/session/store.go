package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Context
}

func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Context),
	}
}

func (s *Store) Create(cwd string) (*Context, error) {
	id := generateSessionID()
	ctx := NewContext(id, cwd)
	s.mu.Lock()
	s.sessions[id] = ctx
	s.mu.Unlock()
	return ctx, nil
}

func (s *Store) Get(id string) (*Context, bool) {
	s.mu.RLock()
	ctx, ok := s.sessions[id]
	s.mu.RUnlock()
	return ctx, ok
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	if ctx, ok := s.sessions[id]; ok {
		ctx.Close()
		delete(s.sessions, id)
	}
	s.mu.Unlock()
}

func (s *Store) CloseAll() {
	s.mu.Lock()
	for id, ctx := range s.sessions {
		ctx.Close()
		delete(s.sessions, id)
	}
	s.mu.Unlock()
}

func generateSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(b[:])
}
