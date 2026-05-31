package dns

import (
	"context"
	"errors"
	"testing"
)

func TestMemory_ZoneCRUD(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()

	z1, err := s.CreateZone(ctx, Zone{UUID: "z1", Name: "weft.internal", Project: "platform", Role: "primary", CreatedAtNs: 1})
	if err != nil {
		t.Fatalf("CreateZone : %v", err)
	}
	if z1.UUID != "z1" {
		t.Errorf("CreateZone returned wrong zone : %+v", z1)
	}
	if _, err := s.CreateZone(ctx, Zone{UUID: "z2", Name: "acme.weft.internal", Project: "team-alpha", CreatedAtNs: 2}); err != nil {
		t.Fatalf("CreateZone z2 : %v", err)
	}

	all, _ := s.ListZones(ctx, ZoneFilter{})
	if len(all) != 2 || all[0].UUID != "z1" || all[1].UUID != "z2" {
		t.Errorf("ListZones(all) = %v ; want [z1,z2]", zoneUUIDs(all))
	}
	scoped, _ := s.ListZones(ctx, ZoneFilter{Project: "platform"})
	if len(scoped) != 1 || scoped[0].UUID != "z1" {
		t.Errorf("ListZones(project=platform) = %v ; want [z1]", zoneUUIDs(scoped))
	}

	if err := s.DeleteZone(ctx, "z2"); err != nil {
		t.Fatalf("DeleteZone : %v", err)
	}
	if _, err := s.GetZone(ctx, "z2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetZone after delete = %v ; want ErrNotFound", err)
	}
}

func TestMemory_ZoneDuplicateInProject(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if _, err := s.CreateZone(ctx, Zone{UUID: "a", Name: "dup", Project: "p", CreatedAtNs: 1}); err != nil {
		t.Fatalf("Create : %v", err)
	}
	_, err := s.CreateZone(ctx, Zone{UUID: "b", Name: "dup", Project: "p", CreatedAtNs: 2})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate (project,name) = %v ; want ErrAlreadyExists", err)
	}
	// Different project = no collision.
	if _, err := s.CreateZone(ctx, Zone{UUID: "c", Name: "dup", Project: "other", CreatedAtNs: 3}); err != nil {
		t.Errorf("same name in other project should be allowed : %v", err)
	}
}

func TestMemory_RecordCRUDWithZone(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	z, _ := s.CreateZone(ctx, Zone{UUID: "z1", Name: "weft.internal", Project: "platform", CreatedAtNs: 1})

	r1, err := s.CreateRecord(ctx, Record{UUID: "r1", ZoneUUID: z.UUID, Name: "alpha", Type: "A", Value: "10.0.0.1", TTL: 300, Source: "static"})
	if err != nil {
		t.Fatalf("CreateRecord : %v", err)
	}
	if r1.ZoneName != "weft.internal" {
		t.Errorf("CreateRecord didn't denormalise ZoneName ; got %q", r1.ZoneName)
	}

	// Record count is denormalised onto the zone.
	zReread, _ := s.GetZone(ctx, "z1")
	if zReread.Records != 1 {
		t.Errorf("Zone.Records after 1 create = %d ; want 1", zReread.Records)
	}

	if _, err := s.CreateRecord(ctx, Record{UUID: "r2", ZoneUUID: z.UUID, Name: "beta", Type: "A", Value: "10.0.0.2"}); err != nil {
		t.Fatalf("CreateRecord r2 : %v", err)
	}
	zReread, _ = s.GetZone(ctx, "z1")
	if zReread.Records != 2 {
		t.Errorf("Zone.Records after 2 creates = %d ; want 2", zReread.Records)
	}

	all, _ := s.ListRecords(ctx, RecordFilter{ZoneUUID: "z1"})
	if len(all) != 2 {
		t.Errorf("ListRecords(zone=z1) = %d ; want 2", len(all))
	}

	if err := s.DeleteRecord(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRecord : %v", err)
	}
	zReread, _ = s.GetZone(ctx, "z1")
	if zReread.Records != 1 {
		t.Errorf("Zone.Records after 1 delete = %d ; want 1", zReread.Records)
	}
}

func TestMemory_RecordRequiresExistingZone(t *testing.T) {
	s := NewMemory()
	_, err := s.CreateRecord(context.Background(), Record{UUID: "r1", ZoneUUID: "no-such", Name: "a", Type: "A", Value: "10.0.0.1"})
	if !errors.Is(err, ErrZoneNotFound) {
		t.Errorf("record under missing zone = %v ; want ErrZoneNotFound", err)
	}
}

func TestMemory_DeleteZoneCascadesRecords(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	z, _ := s.CreateZone(ctx, Zone{UUID: "z1", Name: "weft.internal", Project: "p", CreatedAtNs: 1})
	_, _ = s.CreateRecord(ctx, Record{UUID: "r1", ZoneUUID: z.UUID, Name: "a", Type: "A", Value: "10.0.0.1"})
	_, _ = s.CreateRecord(ctx, Record{UUID: "r2", ZoneUUID: z.UUID, Name: "b", Type: "A", Value: "10.0.0.2"})

	if err := s.DeleteZone(ctx, "z1"); err != nil {
		t.Fatalf("DeleteZone : %v", err)
	}
	// Both records are gone.
	for _, uuid := range []string{"r1", "r2"} {
		if _, err := s.GetRecord(ctx, uuid); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRecord %s after zone delete = %v ; want ErrNotFound", uuid, err)
		}
	}
	// ListRecords zone-scoped returns empty.
	rs, _ := s.ListRecords(ctx, RecordFilter{ZoneUUID: "z1"})
	if len(rs) != 0 {
		t.Errorf("ListRecords after zone delete = %d ; want 0", len(rs))
	}
}

func TestMemory_DeleteMissing(t *testing.T) {
	s := NewMemory()
	if err := s.DeleteZone(context.Background(), "no-such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteZone missing = %v ; want ErrNotFound", err)
	}
	if err := s.DeleteRecord(context.Background(), "no-such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRecord missing = %v ; want ErrNotFound", err)
	}
}

func TestMemory_ToProtoRoundTrips(t *testing.T) {
	z := Zone{
		UUID: "z1", Name: "weft.internal", Role: "primary", Records: 5, TTLDefault: 300,
		Backend: "coredns", PushTarget: "ns1.example", PushState: "synced @ 2026-05-31",
		Project: "platform", Status: "active", CreatedAtNs: 12345,
	}
	pz := z.ToProto()
	if pz.Name != z.Name || pz.Records != z.Records || pz.CreatedAtUnixNs != z.CreatedAtNs ||
		pz.Backend != z.Backend || pz.PushTarget != z.PushTarget {
		t.Errorf("Zone.ToProto field mismatch ; got %+v", pz)
	}
	r := Record{
		UUID: "r1", ZoneUUID: "z1", ZoneName: "weft.internal", Name: "alpha",
		Type: "A", Value: "10.0.0.1", TTL: 300, Source: "static",
	}
	pr := r.ToProto()
	if pr.Name != r.Name || pr.Value != r.Value || pr.Zone != r.ZoneName || pr.Ttl != r.TTL {
		t.Errorf("Record.ToProto field mismatch ; got %+v", pr)
	}
}

func zoneUUIDs(zs []Zone) []string {
	out := make([]string, len(zs))
	for i, z := range zs {
		out[i] = z.UUID
	}
	return out
}
