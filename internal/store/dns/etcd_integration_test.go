package dns

import (
	"context"
	"errors"
	"testing"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

// TestEtcd_DNSListAndFilters exercises the prefix-scan paths that the
// single happy-path test doesn't visit : List with empty store, List
// across projects + filtered, ListRecords filtered by ZoneUUID, and
// GetZone / GetRecord of-missing returning ErrNotFound.
func TestEtcd_DNSListAndFilters(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	// List against an empty prefix : zero entries, no error.
	zs, err := s.ListZones(ctx, ZoneFilter{})
	if err != nil {
		t.Fatalf("ListZones empty : %v", err)
	}
	if len(zs) != 0 {
		t.Errorf("ListZones empty = %d ; want 0", len(zs))
	}
	rs, err := s.ListRecords(ctx, RecordFilter{})
	if err != nil {
		t.Fatalf("ListRecords empty : %v", err)
	}
	if len(rs) != 0 {
		t.Errorf("ListRecords empty = %d ; want 0", len(rs))
	}

	// GetZone / GetRecord of-missing.
	if _, err := s.GetZone(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetZone missing = %v ; want ErrNotFound", err)
	}
	if _, err := s.GetRecord(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRecord missing = %v ; want ErrNotFound", err)
	}

	// Seed two zones across two projects so ListZones filter can be
	// exercised meaningfully.
	zA, err := s.CreateZone(ctx, Zone{UUID: "zA", Name: "a.internal", Project: "p1", CreatedAtNs: 1})
	if err != nil {
		t.Fatalf("CreateZone zA : %v", err)
	}
	if _, err := s.CreateZone(ctx, Zone{UUID: "zB", Name: "b.internal", Project: "p2", CreatedAtNs: 2}); err != nil {
		t.Fatalf("CreateZone zB : %v", err)
	}

	// Duplicate (project, name) is rejected ; cross-project same name
	// is allowed.
	if _, err := s.CreateZone(ctx, Zone{UUID: "zDup", Name: "a.internal", Project: "p1", CreatedAtNs: 3}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup zone = %v ; want ErrAlreadyExists", err)
	}
	if _, err := s.CreateZone(ctx, Zone{UUID: "zC", Name: "a.internal", Project: "other", CreatedAtNs: 4}); err != nil {
		t.Errorf("cross-project same name : %v", err)
	}

	// ListZones unfiltered : all three.
	all, _ := s.ListZones(ctx, ZoneFilter{})
	if len(all) != 3 {
		t.Errorf("ListZones all = %d ; want 3", len(all))
	}
	// ListZones filtered by project : only p1.
	scoped, _ := s.ListZones(ctx, ZoneFilter{Project: "p1"})
	if len(scoped) != 1 || scoped[0].UUID != "zA" {
		t.Errorf("ListZones(p1) = %v ; want [zA]", zoneUUIDsOf(scoped))
	}

	// Records under zA — exercise ListRecords filter + DeleteRecord
	// paths.
	if _, err := s.CreateRecord(ctx, Record{UUID: "r1", ZoneUUID: zA.UUID, Name: "alpha", Type: "A", Value: "10.0.0.1"}); err != nil {
		t.Fatalf("CreateRecord r1 : %v", err)
	}
	if _, err := s.CreateRecord(ctx, Record{UUID: "r2", ZoneUUID: zA.UUID, Name: "beta", Type: "A", Value: "10.0.0.2"}); err != nil {
		t.Fatalf("CreateRecord r2 : %v", err)
	}
	// Unfiltered List : both.
	rsAll, _ := s.ListRecords(ctx, RecordFilter{})
	if len(rsAll) != 2 {
		t.Errorf("ListRecords all = %d ; want 2", len(rsAll))
	}
	// Filtered by zone : both (only one zone has records).
	rsZA, _ := s.ListRecords(ctx, RecordFilter{ZoneUUID: zA.UUID})
	if len(rsZA) != 2 {
		t.Errorf("ListRecords(zA) = %d ; want 2", len(rsZA))
	}
	// Filtered by a zone with no records : zero.
	rsZB, _ := s.ListRecords(ctx, RecordFilter{ZoneUUID: "zB"})
	if len(rsZB) != 0 {
		t.Errorf("ListRecords(zB) = %d ; want 0", len(rsZB))
	}

	// GetRecord happy path — confirms ZoneName denormalisation
	// survived JSON round-trip.
	got, err := s.GetRecord(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRecord : %v", err)
	}
	if got.ZoneName != "a.internal" {
		t.Errorf("GetRecord ZoneName = %q ; want a.internal", got.ZoneName)
	}

	// DeleteRecord happy path : record gone, zone count decremented.
	if err := s.DeleteRecord(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRecord : %v", err)
	}
	if _, err := s.GetRecord(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRecord after delete = %v ; want ErrNotFound", err)
	}
	zCheck, _ := s.GetZone(ctx, zA.UUID)
	if zCheck.Records != 1 {
		t.Errorf("Zone.Records after delete = %d ; want 1", zCheck.Records)
	}
	// DeleteRecord-of-missing : ErrNotFound (GetRecord short-circuits).
	if err := s.DeleteRecord(ctx, "no-such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRecord missing = %v ; want ErrNotFound", err)
	}
}

func zoneUUIDsOf(zs []Zone) []string {
	out := make([]string, len(zs))
	for i, z := range zs {
		out[i] = z.UUID
	}
	return out
}

func TestEtcd_DNSZonesAndRecordsAgainstEmbeddedEtcd(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	// Create a zone, then records under it. Zone.Records should
	// auto-increment as records get added.
	z, err := s.CreateZone(ctx, Zone{
		UUID: "z1", Name: "weft.internal", Project: "platform", Role: "primary",
		Backend: "coredns", CreatedAtNs: 1,
	})
	if err != nil {
		t.Fatalf("CreateZone : %v", err)
	}

	r1, err := s.CreateRecord(ctx, Record{
		UUID: "r1", ZoneUUID: z.UUID, Name: "alpha", Type: "A", Value: "10.0.0.1", Source: "static",
	})
	if err != nil {
		t.Fatalf("CreateRecord : %v", err)
	}
	if r1.ZoneName != "weft.internal" {
		t.Errorf("CreateRecord didn't denormalise ZoneName ; got %q", r1.ZoneName)
	}
	if _, err := s.CreateRecord(ctx, Record{
		UUID: "r2", ZoneUUID: z.UUID, Name: "beta", Type: "A", Value: "10.0.0.2",
	}); err != nil {
		t.Fatalf("CreateRecord r2 : %v", err)
	}

	// Zone.Records is denormalised — re-read it to check.
	z2, err := s.GetZone(ctx, "z1")
	if err != nil {
		t.Fatalf("GetZone : %v", err)
	}
	if z2.Records != 2 {
		t.Errorf("Zone.Records after 2 inserts = %d ; want 2", z2.Records)
	}

	// Record under missing zone : ErrZoneNotFound.
	if _, err := s.CreateRecord(ctx, Record{
		UUID: "rX", ZoneUUID: "no-such", Name: "x", Type: "A", Value: "1.1.1.1",
	}); !errors.Is(err, ErrZoneNotFound) {
		t.Errorf("record under missing zone = %v ; want ErrZoneNotFound", err)
	}

	// Cascade : DeleteZone removes all its records atomically.
	if err := s.DeleteZone(ctx, z.UUID); err != nil {
		t.Fatalf("DeleteZone : %v", err)
	}
	for _, uuid := range []string{"r1", "r2"} {
		if _, err := s.GetRecord(ctx, uuid); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRecord %s after cascade = %v ; want ErrNotFound", uuid, err)
		}
	}
	if _, err := s.GetZone(ctx, z.UUID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetZone after delete = %v ; want ErrNotFound", err)
	}

	// Cascade-of-missing : ErrNotFound (the Txn predicate fails).
	if err := s.DeleteZone(ctx, "no-such"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteZone missing = %v ; want ErrNotFound", err)
	}
}
