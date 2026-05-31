package lb

import (
	"context"
	"sort"
	"sync"
)

type memoryStore struct {
	mu       sync.Mutex
	byUUID   map[string]LoadBalancer
	byPjName map[string]string
}

// NewMemory builds an empty in-memory LB store.
func NewMemory() Store {
	return &memoryStore{
		byUUID:   map[string]LoadBalancer{},
		byPjName: map[string]string{},
	}
}

func key(project, name string) string { return project + "|" + name }

func (s *memoryStore) List(_ context.Context, f ListFilter) ([]LoadBalancer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LoadBalancer, 0, len(s.byUUID))
	for _, l := range s.byUUID {
		if f.Project != "" && l.Project != f.Project {
			continue
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtNs != out[j].CreatedAtNs {
			return out[i].CreatedAtNs < out[j].CreatedAtNs
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *memoryStore) Create(_ context.Context, l LoadBalancer) (LoadBalancer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(l.Project, l.Name)
	if _, exists := s.byPjName[k]; exists {
		return LoadBalancer{}, ErrAlreadyExists
	}
	s.byUUID[l.UUID] = l
	s.byPjName[k] = l.UUID
	return l, nil
}

func (s *memoryStore) Delete(_ context.Context, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byUUID[uuid]
	if !ok {
		return ErrNotFound
	}
	delete(s.byUUID, uuid)
	delete(s.byPjName, key(l.Project, l.Name))
	return nil
}

func (s *memoryStore) Get(_ context.Context, uuid string) (LoadBalancer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byUUID[uuid]
	if !ok {
		return LoadBalancer{}, ErrNotFound
	}
	return l, nil
}

func (s *memoryStore) SetBackends(_ context.Context, uuid string, backends []string) (LoadBalancer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byUUID[uuid]
	if !ok {
		return LoadBalancer{}, ErrNotFound
	}
	l.Backends = append([]string(nil), backends...)
	s.byUUID[uuid] = l
	return l, nil
}
