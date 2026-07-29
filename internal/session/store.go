package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
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
	id, err := generateSessionID()
	if err != nil {
		return nil, err
	}
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

func (s *Store) CloseAll() []*Context {
	s.mu.Lock()
	closed := make([]*Context, 0, len(s.sessions))
	for id, ctx := range s.sessions {
		ctx.Close()
		closed = append(closed, ctx)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return closed
}

func generateSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return "sess_" + hex.EncodeToString(b[:]), nil
}
