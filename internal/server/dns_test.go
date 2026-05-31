package server

import (
	"context"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// helper : create one zone and return its uuid.
func mustCreateZone(t *testing.T, s *Server, name, project string) string {
	t.Helper()
	r, err := s.CreateDNSZone(context.Background(), &netv1.CreateDNSZoneRequest{
		Name: name, Project: project,
	})
	if err != nil {
		t.Fatalf("CreateDNSZone(%q) : %v", name, err)
	}
	return r.GetZone().GetUuid()
}

func TestDNSZones_CreateListDelete(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	cr, err := s.CreateDNSZone(ctx, &netv1.CreateDNSZoneRequest{
		Project: "platform", Name: "weft.internal", Role: "primary", TtlDefault: 60,
	})
	if err != nil {
		t.Fatalf("CreateDNSZone : %v", err)
	}
	z := cr.GetZone()
	if z.GetUuid() == "" || z.GetBackend() != "coredns" || z.GetTtlDefault() != 60 || z.GetStatus() != "syncing" {
		t.Errorf("CreateDNSZone returned unexpected zone : %+v", z)
	}

	// Role default = "primary" when empty.
	cr2, _ := s.CreateDNSZone(ctx, &netv1.CreateDNSZoneRequest{Name: "acme.weft.internal", Project: "team-alpha"})
	if cr2.GetZone().GetRole() != "primary" {
		t.Errorf("default role = %q ; want primary", cr2.GetZone().GetRole())
	}
	// TTL default = 300 when not given.
	if cr2.GetZone().GetTtlDefault() != 300 {
		t.Errorf("default ttl = %d ; want 300", cr2.GetZone().GetTtlDefault())
	}

	// Project scoping.
	ls, _ := s.ListDNSZones(ctx, &netv1.ListDNSZonesRequest{Project: "platform"})
	if len(ls.GetZones()) != 1 {
		t.Errorf("ListDNSZones(project=platform) = %d ; want 1", len(ls.GetZones()))
	}
	all, _ := s.ListDNSZones(ctx, &netv1.ListDNSZonesRequest{})
	if len(all.GetZones()) != 2 {
		t.Errorf("ListDNSZones(all) = %d ; want 2", len(all.GetZones()))
	}

	if _, err := s.DeleteDNSZone(ctx, &netv1.DeleteDNSZoneRequest{Uuid: z.GetUuid()}); err != nil {
		t.Fatalf("DeleteDNSZone : %v", err)
	}
}

func TestDNSZones_CreateValidation(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	cases := []struct {
		name string
		req  *netv1.CreateDNSZoneRequest
		code codes.Code
	}{
		{"empty name", &netv1.CreateDNSZoneRequest{}, codes.InvalidArgument},
		{"whitespace name", &netv1.CreateDNSZoneRequest{Name: "   "}, codes.InvalidArgument},
		{"bad role", &netv1.CreateDNSZoneRequest{Name: "z", Role: "primaryyy"}, codes.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateDNSZone(ctx, c.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if st, _ := status.FromError(err); st.Code() != c.code {
				t.Errorf("code = %s ; want %s", st.Code(), c.code)
			}
		})
	}
}

func TestDNSZones_DuplicateNameInProject(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	req := &netv1.CreateDNSZoneRequest{Project: "p", Name: "dup"}
	if _, err := s.CreateDNSZone(ctx, req); err != nil {
		t.Fatalf("first Create : %v", err)
	}
	_, err := s.CreateDNSZone(ctx, req)
	if err == nil {
		t.Fatal("expected AlreadyExists")
	}
	if st, _ := status.FromError(err); st.Code() != codes.AlreadyExists {
		t.Errorf("code = %s ; want AlreadyExists", st.Code())
	}
}

func TestDNSZones_DeleteMissing(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteDNSZone(context.Background(), &netv1.DeleteDNSZoneRequest{Uuid: "no-such"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code = %s ; want NotFound", st.Code())
	}
}

func TestDNSRecords_CreateListDelete(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	zUUID := mustCreateZone(t, s, "weft.internal", "platform")

	cr, err := s.CreateDNSRecord(ctx, &netv1.CreateDNSRecordRequest{
		ZoneUuid: zUUID, Name: "alpha", Type: "A", Value: "10.0.0.1", Ttl: 60,
	})
	if err != nil {
		t.Fatalf("CreateDNSRecord : %v", err)
	}
	if cr.GetRecord().GetSource() != "static" {
		t.Errorf("API-created records must be static ; got %q", cr.GetRecord().GetSource())
	}
	if cr.GetRecord().GetZone() != "weft.internal" {
		t.Errorf("zone denormalisation missing ; got %q", cr.GetRecord().GetZone())
	}

	// Type lower-case in request must be normalised on the way in.
	cr2, _ := s.CreateDNSRecord(ctx, &netv1.CreateDNSRecordRequest{
		ZoneUuid: zUUID, Name: "beta", Type: "a", Value: "10.0.0.2",
	})
	if cr2.GetRecord().GetType() != "A" {
		t.Errorf("type not upper-cased ; got %q", cr2.GetRecord().GetType())
	}

	// Default TTL inherits the zone's default (which itself defaulted to 300).
	if cr2.GetRecord().GetTtl() != 300 {
		t.Errorf("default record TTL = %d ; want zone default 300", cr2.GetRecord().GetTtl())
	}

	ls, _ := s.ListDNSRecords(ctx, &netv1.ListDNSRecordsRequest{ZoneUuid: zUUID})
	if len(ls.GetRecords()) != 2 {
		t.Errorf("ListDNSRecords(zone) = %d ; want 2", len(ls.GetRecords()))
	}

	if _, err := s.DeleteDNSRecord(ctx, &netv1.DeleteDNSRecordRequest{Uuid: cr.GetRecord().GetUuid()}); err != nil {
		t.Fatalf("DeleteDNSRecord : %v", err)
	}
}

func TestDNSRecords_CreateValidation(t *testing.T) {
	s := New(Options{})
	zUUID := mustCreateZone(t, s, "z", "p")
	cases := []struct {
		name string
		req  *netv1.CreateDNSRecordRequest
		code codes.Code
	}{
		{"empty zone_uuid", &netv1.CreateDNSRecordRequest{Name: "a", Type: "A", Value: "1.1.1.1"}, codes.InvalidArgument},
		{"bad type", &netv1.CreateDNSRecordRequest{ZoneUuid: zUUID, Name: "a", Type: "PTR", Value: "x"}, codes.InvalidArgument},
		{"empty name", &netv1.CreateDNSRecordRequest{ZoneUuid: zUUID, Type: "A", Value: "1.1.1.1"}, codes.InvalidArgument},
		{"empty value", &netv1.CreateDNSRecordRequest{ZoneUuid: zUUID, Name: "a", Type: "A"}, codes.InvalidArgument},
		{"missing zone", &netv1.CreateDNSRecordRequest{ZoneUuid: "no-such", Name: "a", Type: "A", Value: "1.1.1.1"}, codes.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateDNSRecord(context.Background(), c.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if st, _ := status.FromError(err); st.Code() != c.code {
				t.Errorf("code = %s ; want %s", st.Code(), c.code)
			}
		})
	}
}

func TestDNSRecords_DeleteMissing(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteDNSRecord(context.Background(), &netv1.DeleteDNSRecordRequest{Uuid: "nope"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code = %s ; want NotFound", st.Code())
	}
}

func TestDNSZoneDelete_CascadesRecords(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	zUUID := mustCreateZone(t, s, "weft.internal", "platform")
	r1, _ := s.CreateDNSRecord(ctx, &netv1.CreateDNSRecordRequest{ZoneUuid: zUUID, Name: "a", Type: "A", Value: "10.0.0.1"})
	r2, _ := s.CreateDNSRecord(ctx, &netv1.CreateDNSRecordRequest{ZoneUuid: zUUID, Name: "b", Type: "A", Value: "10.0.0.2"})

	if _, err := s.DeleteDNSZone(ctx, &netv1.DeleteDNSZoneRequest{Uuid: zUUID}); err != nil {
		t.Fatalf("DeleteDNSZone : %v", err)
	}
	// Both records gone from list.
	ls, _ := s.ListDNSRecords(ctx, &netv1.ListDNSRecordsRequest{})
	if len(ls.GetRecords()) != 0 {
		t.Errorf("ListDNSRecords after cascade delete = %d ; want 0", len(ls.GetRecords()))
	}
	// Pointed deletes are NotFound.
	for _, r := range []*netv1.CreateDNSRecordResponse{r1, r2} {
		_, err := s.DeleteDNSRecord(ctx, &netv1.DeleteDNSRecordRequest{Uuid: r.GetRecord().GetUuid()})
		if st, _ := status.FromError(err); st.Code() != codes.NotFound {
			t.Errorf("post-cascade DeleteDNSRecord = %s ; want NotFound", st.Code())
		}
	}
}
