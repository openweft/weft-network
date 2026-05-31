package router

import (
	"context"
	"sort"
	"sync"
)

type memoryStore struct {
	mu       sync.Mutex
	byUUID   map[string]Router
	byPjName map[string]string
}

// NewMemory builds an empty in-memory router store.
func NewMemory() Store {
	return &memoryStore{
		byUUID:   map[string]Router{},
		byPjName: map[string]string{},
	}
}

func key(project, name string) string { return project + "|" + name }

func (s *memoryStore) List(_ context.Context, f ListFilter) ([]Router, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Router, 0, len(s.byUUID))
	for _, r := range s.byUUID {
		if f.Project != "" && r.Project != f.Project {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtNs != out[j].CreatedAtNs {
			return out[i].CreatedAtNs < out[j].CreatedAtNs
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *memoryStore) Create(_ context.Context, r Router) (Router, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(r.Project, r.Name)
	if _, exists := s.byPjName[k]; exists {
		return Router{}, ErrAlreadyExists
	}
	s.byUUID[r.UUID] = r
	s.byPjName[k] = r.UUID
	return r, nil
}

func (s *memoryStore) Delete(_ context.Context, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byUUID[uuid]
	if !ok {
		return ErrNotFound
	}
	delete(s.byUUID, uuid)
	delete(s.byPjName, key(r.Project, r.Name))
	return nil
}

func (s *memoryStore) Get(_ context.Context, uuid string) (Router, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byUUID[uuid]
	if !ok {
		return Router{}, ErrNotFound
	}
	return r, nil
}
