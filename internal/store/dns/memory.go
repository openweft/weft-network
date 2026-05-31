package dns

import (
	"context"
	"sort"
	"sync"
)

// memoryStore is the in-process backend selected when the daemon
// runs without --etcd. State lives in maps under a single mutex.
//
// Foreign-key invariants live HERE (CreateRecord checks the zone,
// DeleteZone cascades to records). When the etcd backend lands it
// honours the same contract — the etcd transaction wraps both writes.
type memoryStore struct {
	mu          sync.Mutex
	zones       map[string]Zone   // uuid → Zone
	zoneByName  map[string]string // "<project>|<name>" → uuid
	records     map[string]Record // uuid → Record
	recordsByZ  map[string]map[string]struct{} // zoneUUID → set of recordUUID
}

// NewMemory builds an empty in-memory DNS store.
func NewMemory() Store {
	return &memoryStore{
		zones:      map[string]Zone{},
		zoneByName: map[string]string{},
		records:    map[string]Record{},
		recordsByZ: map[string]map[string]struct{}{},
	}
}

func zoneKey(project, name string) string { return project + "|" + name }

// ---- Zones -------------------------------------------------------

func (s *memoryStore) ListZones(_ context.Context, f ZoneFilter) ([]Zone, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Zone, 0, len(s.zones))
	for _, z := range s.zones {
		if f.Project != "" && z.Project != f.Project {
			continue
		}
		out = append(out, z)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtNs != out[j].CreatedAtNs {
			return out[i].CreatedAtNs < out[j].CreatedAtNs
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *memoryStore) CreateZone(_ context.Context, z Zone) (Zone, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := zoneKey(z.Project, z.Name)
	if _, exists := s.zoneByName[key]; exists {
		return Zone{}, ErrAlreadyExists
	}
	s.zones[z.UUID] = z
	s.zoneByName[key] = z.UUID
	s.recordsByZ[z.UUID] = map[string]struct{}{}
	return z, nil
}

func (s *memoryStore) DeleteZone(_ context.Context, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	z, ok := s.zones[uuid]
	if !ok {
		return ErrNotFound
	}
	// Cascade : drop every record under this zone. Operator just
	// nuked the namespace, the records can't survive in isolation.
	for rUUID := range s.recordsByZ[uuid] {
		delete(s.records, rUUID)
	}
	delete(s.recordsByZ, uuid)
	delete(s.zones, uuid)
	delete(s.zoneByName, zoneKey(z.Project, z.Name))
	return nil
}

func (s *memoryStore) GetZone(_ context.Context, uuid string) (Zone, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z, ok := s.zones[uuid]
	if !ok {
		return Zone{}, ErrNotFound
	}
	return z, nil
}

// ---- Records -----------------------------------------------------

func (s *memoryStore) ListRecords(_ context.Context, f RecordFilter) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		if f.ZoneUUID != "" && r.ZoneUUID != f.ZoneUUID {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ZoneName != out[j].ZoneName {
			return out[i].ZoneName < out[j].ZoneName
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

func (s *memoryStore) CreateRecord(_ context.Context, r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z, ok := s.zones[r.ZoneUUID]
	if !ok {
		return Record{}, ErrZoneNotFound
	}
	r.ZoneName = z.Name
	s.records[r.UUID] = r
	s.recordsByZ[r.ZoneUUID][r.UUID] = struct{}{}
	// Bump the denormalised count so ListZones reflects reality.
	z.Records = int32(len(s.recordsByZ[r.ZoneUUID]))
	s.zones[r.ZoneUUID] = z
	return r, nil
}

func (s *memoryStore) DeleteRecord(_ context.Context, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[uuid]
	if !ok {
		return ErrNotFound
	}
	delete(s.records, uuid)
	if zSet, ok := s.recordsByZ[r.ZoneUUID]; ok {
		delete(zSet, uuid)
		if z, zOk := s.zones[r.ZoneUUID]; zOk {
			z.Records = int32(len(zSet))
			s.zones[r.ZoneUUID] = z
		}
	}
	return nil
}

func (s *memoryStore) GetRecord(_ context.Context, uuid string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[uuid]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}
