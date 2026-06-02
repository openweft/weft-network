package dns

import (
	"context"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// etcd layout :
//
//   /weft/network/dns-zones/<uuid>    = JSON Zone
//   /weft/network/dns-records/<uuid>  = JSON Record
//
// Cross-collection references (Record.ZoneUUID → Zone) are denormalised
// on write : CreateRecord copies the Zone.Name into Record.ZoneName so
// List doesn't need a JOIN. DeleteZone cascades by enumerating records
// with a matching ZoneUUID — O(N) in record count, fine while N is
// in the hundreds. Trade up for a /by-zone-uuid/<zone>/<uuid> mirror
// when the cascade becomes the bottleneck.
const (
	etcdZonePrefix   = "/weft/network/dns-zones/"
	etcdRecordPrefix = "/weft/network/dns-records/"
)

type etcdStore struct {
	client *clientv3.Client
}

// NewEtcd builds an etcd-backed DNS store.
func NewEtcd(client *clientv3.Client) Store {
	return &etcdStore{client: client}
}

// etcdZoneKey / etcdRecordKey build the on-disk paths. Named with
// the etcd prefix to avoid colliding with memoryStore's zoneKey
// (which is the (project, name) compound key it uses for its
// uniqueness index).
func etcdZoneKey(uuid string) string   { return etcdZonePrefix + uuid }
func etcdRecordKey(uuid string) string { return etcdRecordPrefix + uuid }

// ---- Zones -------------------------------------------------------

func (s *etcdStore) ListZones(ctx context.Context, f ZoneFilter) ([]Zone, error) {
	resp, err := s.client.Get(ctx, etcdZonePrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd list zones : %w", err)
	}
	out := make([]Zone, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var z Zone
		if json.Unmarshal(kv.Value, &z) != nil {
			continue
		}
		if f.Project != "" && z.Project != f.Project {
			continue
		}
		out = append(out, z)
	}
	return out, nil
}

func (s *etcdStore) CreateZone(ctx context.Context, z Zone) (Zone, error) {
	// (project, name) uniqueness scan.
	zones, err := s.client.Get(ctx, etcdZonePrefix, clientv3.WithPrefix())
	if err != nil {
		return Zone{}, fmt.Errorf("etcd zone uniqueness scan : %w", err)
	}
	for _, kv := range zones.Kvs {
		var existing Zone
		if json.Unmarshal(kv.Value, &existing) != nil {
			continue
		}
		if existing.Project == z.Project && existing.Name == z.Name {
			return Zone{}, ErrAlreadyExists
		}
	}
	encoded, err := json.Marshal(z)
	if err != nil {
		return Zone{}, fmt.Errorf("encode zone : %w", err)
	}
	txn := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(etcdZoneKey(z.UUID)), "=", 0)).
		Then(clientv3.OpPut(etcdZoneKey(z.UUID), string(encoded)))
	tr, err := txn.Commit()
	if err != nil {
		return Zone{}, fmt.Errorf("etcd put zone : %w", err)
	}
	if !tr.Succeeded {
		return Zone{}, fmt.Errorf("dns zone uuid %q already exists in etcd", z.UUID)
	}
	return z, nil
}

func (s *etcdStore) DeleteZone(ctx context.Context, uuid string) error {
	// Cascade : enumerate records pointing at this zone, delete them
	// in the same Txn as the zone itself so a concurrent read can't
	// see a record whose zone has been removed.
	recs, err := s.client.Get(ctx, etcdRecordPrefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("etcd list records for cascade : %w", err)
	}
	ops := []clientv3.Op{clientv3.OpDelete(etcdZoneKey(uuid))}
	for _, kv := range recs.Kvs {
		var r Record
		if json.Unmarshal(kv.Value, &r) != nil {
			continue
		}
		if r.ZoneUUID == uuid {
			ops = append(ops, clientv3.OpDelete(string(kv.Key)))
		}
	}
	// Guard the zone existence with a Compare so a Delete-of-nothing
	// surfaces as ErrNotFound rather than silently dropping the
	// cascade ops.
	tr, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(etcdZoneKey(uuid)), "!=", 0)).
		Then(ops...).
		Commit()
	if err != nil {
		return fmt.Errorf("etcd cascade delete zone : %w", err)
	}
	if !tr.Succeeded {
		return ErrNotFound
	}
	return nil
}

func (s *etcdStore) GetZone(ctx context.Context, uuid string) (Zone, error) {
	resp, err := s.client.Get(ctx, etcdZoneKey(uuid))
	if err != nil {
		return Zone{}, fmt.Errorf("etcd get zone : %w", err)
	}
	if len(resp.Kvs) == 0 {
		return Zone{}, ErrNotFound
	}
	var z Zone
	if err := json.Unmarshal(resp.Kvs[0].Value, &z); err != nil {
		return Zone{}, fmt.Errorf("decode zone %s : %w", uuid, err)
	}
	return z, nil
}

// ---- Records -----------------------------------------------------

func (s *etcdStore) ListRecords(ctx context.Context, f RecordFilter) ([]Record, error) {
	resp, err := s.client.Get(ctx, etcdRecordPrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd list records : %w", err)
	}
	out := make([]Record, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var r Record
		if json.Unmarshal(kv.Value, &r) != nil {
			continue
		}
		if f.ZoneUUID != "" && r.ZoneUUID != f.ZoneUUID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *etcdStore) CreateRecord(ctx context.Context, r Record) (Record, error) {
	z, err := s.GetZone(ctx, r.ZoneUUID)
	if err != nil {
		if err == ErrNotFound {
			return Record{}, ErrZoneNotFound
		}
		return Record{}, err
	}
	r.ZoneName = z.Name
	encoded, err := json.Marshal(r)
	if err != nil {
		return Record{}, fmt.Errorf("encode record : %w", err)
	}
	tr, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(etcdRecordKey(r.UUID)), "=", 0)).
		Then(clientv3.OpPut(etcdRecordKey(r.UUID), string(encoded))).
		Commit()
	if err != nil {
		return Record{}, fmt.Errorf("etcd put record : %w", err)
	}
	if !tr.Succeeded {
		return Record{}, fmt.Errorf("dns record uuid %q already exists in etcd", r.UUID)
	}
	// Bump the zone's denormalised record count. Best-effort — a
	// failure here doesn't roll back the record write ; the count is
	// just a UX nicety, not load-bearing.
	z.Records++
	if zEnc, mErr := json.Marshal(z); mErr == nil {
		_, _ = s.client.Put(ctx, etcdZoneKey(z.UUID), string(zEnc))
	}
	return r, nil
}

func (s *etcdStore) DeleteRecord(ctx context.Context, uuid string) error {
	// Fetch first so we can decrement the zone's record count after.
	got, err := s.GetRecord(ctx, uuid)
	if err != nil {
		return err
	}
	resp, err := s.client.Delete(ctx, etcdRecordKey(uuid))
	if err != nil {
		return fmt.Errorf("etcd delete record : %w", err)
	}
	if resp.Deleted == 0 {
		return ErrNotFound
	}
	// CAS-retry the zone decrement so we don't clobber concurrent updates
	// to other Zone fields (PushTarget, PushState, TTLDefault, ...).
	for i := 0; i < 8; i++ {
		zKey := etcdZoneKey(got.ZoneUUID)
		gr, gErr := s.client.Get(ctx, zKey)
		if gErr != nil || len(gr.Kvs) == 0 {
			break
		}
		var z Zone
		if json.Unmarshal(gr.Kvs[0].Value, &z) != nil {
			break
		}
		if z.Records == 0 {
			break
		}
		z.Records--
		zEnc, mErr := json.Marshal(z)
		if mErr != nil {
			break
		}
		tr, txErr := s.client.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(zKey), "=", gr.Kvs[0].ModRevision)).
			Then(clientv3.OpPut(zKey, string(zEnc))).
			Commit()
		if txErr != nil {
			break
		}
		if tr.Succeeded {
			break
		}
	}
	return nil
}

func (s *etcdStore) GetRecord(ctx context.Context, uuid string) (Record, error) {
	resp, err := s.client.Get(ctx, etcdRecordKey(uuid))
	if err != nil {
		return Record{}, fmt.Errorf("etcd get record : %w", err)
	}
	if len(resp.Kvs) == 0 {
		return Record{}, ErrNotFound
	}
	var r Record
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return Record{}, fmt.Errorf("decode record %s : %w", uuid, err)
	}
	return r, nil
}
