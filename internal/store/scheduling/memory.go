package scheduling

import (
	"context"
	"sort"
	"sync"
)

// memoryStore is the in-process backend selected when the daemon
// runs without --etcd. State lives in maps under a mutex ; restart
// loses everything (call site has to know this).
//
// (project, name) → UUID index supports the uniqueness check inside
// a project without scanning the whole table.
type memoryStore struct {
	mu       sync.Mutex
	byUUID   map[string]Rule
	byPjName map[string]string // "<project>|<name>" → uuid
}

// NewMemory builds an empty in-memory store. Safe to share across
// goroutines.
func NewMemory() Store {
	return &memoryStore{
		byUUID:   map[string]Rule{},
		byPjName: map[string]string{},
	}
}

func projectNameKey(project, name string) string {
	return project + "|" + name
}

func (s *memoryStore) List(_ context.Context, f ListFilter) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, 0, len(s.byUUID))
	for _, r := range s.byUUID {
		if f.Project != "" && r.Project != f.Project {
			continue
		}
		out = append(out, r)
	}
	// Stable ordering : created_at ascending then name as tiebreak.
	// The webui paginates by page_token (RFC-3339 timestamp today —
	// see [[openweft pull model]]) ; a stable List is a precondition.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtNs != out[j].CreatedAtNs {
			return out[i].CreatedAtNs < out[j].CreatedAtNs
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *memoryStore) Create(_ context.Context, r Rule) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := projectNameKey(r.Project, r.Name)
	if _, exists := s.byPjName[key]; exists {
		return Rule{}, ErrAlreadyExists
	}
	s.byUUID[r.UUID] = r
	s.byPjName[key] = r.UUID
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
	delete(s.byPjName, projectNameKey(r.Project, r.Name))
	return nil
}

func (s *memoryStore) Get(_ context.Context, uuid string) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byUUID[uuid]
	if !ok {
		return Rule{}, ErrNotFound
	}
	return r, nil
}
