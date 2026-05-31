package dns

import (
	"context"
	"errors"
	"testing"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

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
