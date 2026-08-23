package token

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps tokens in memory. It is what a service in its "memory"
// storage mode uses, and what the tests here run against.
//
// Tokens do not survive a restart, which for a credential is a defensible
// property in development and an unusable one anywhere else.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[uint]Record
	nextID  uint
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[uint]Record), nextID: 1}
}

func (s *MemoryStore) Insert(_ context.Context, rec Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec.ID = s.nextID
	s.nextID++
	s.records[rec.ID] = rec
	return rec, nil
}

func (s *MemoryStore) ByHash(_ context.Context, hash string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, rec := range s.records {
		if rec.Hash == hash {
			return rec, nil
		}
	}
	return Record{}, ErrNotFound
}

func (s *MemoryStore) BySubject(_ context.Context, subject string) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Record, 0)
	for _, rec := range s.records {
		if rec.Subject == subject {
			out = append(out, rec)
		}
	}
	// Map iteration is randomised, and a token list that reshuffles on every
	// refresh is unusable in a UI.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryStore) Delete(_ context.Context, subject string, id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[id]
	if !ok || rec.Subject != subject {
		// Same answer for "no such token" and "not yours": otherwise the error
		// tells a caller which IDs exist.
		return ErrNotFound
	}
	delete(s.records, id)
	return nil
}

func (s *MemoryStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int64
	for id, rec := range s.records {
		if rec.Expired(before) {
			delete(s.records, id)
			n++
		}
	}
	return n, nil
}
