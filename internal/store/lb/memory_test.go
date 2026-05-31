package lb

import (
	"context"
	"errors"
	"testing"
)

func TestMemory_CRUD(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	a, err := s.Create(ctx, LoadBalancer{
		UUID: "l1", Name: "web-prod", Mode: "L7", Address: "203.0.113.20", Port: 443,
		Backends: []string{"web-1", "web-2"}, AZ: "multi", Project: "team-alpha", CreatedAtNs: 1,
	})
	if err != nil {
		t.Fatalf("Create : %v", err)
	}
	if a.UUID != "l1" {
		t.Errorf("Create returned wrong LB : %+v", a)
	}
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l2", Name: "pg-rw", Mode: "L4", Address: "10.10.0.100", Port: 5432, Project: "team-alpha", CreatedAtNs: 2}); err != nil {
		t.Fatalf("Create l2 : %v", err)
	}
	all, _ := s.List(ctx, ListFilter{Project: "team-alpha"})
	if len(all) != 2 {
		t.Errorf("List(team-alpha) = %d ; want 2", len(all))
	}
	if err := s.Delete(ctx, "l2"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, err := s.Get(ctx, "l2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
}

func TestMemory_DuplicateInProject(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if _, err := s.Create(ctx, LoadBalancer{UUID: "a", Name: "dup", Project: "p", CreatedAtNs: 1}); err != nil {
		t.Fatalf("Create : %v", err)
	}
	_, err := s.Create(ctx, LoadBalancer{UUID: "b", Name: "dup", Project: "p", CreatedAtNs: 2})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup = %v ; want ErrAlreadyExists", err)
	}
}

func TestMemory_SetBackends(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l1", Name: "lb", Mode: "L7", Project: "p", CreatedAtNs: 1, Backends: []string{"a"}}); err != nil {
		t.Fatalf("Create : %v", err)
	}
	upd, err := s.SetBackends(ctx, "l1", []string{"b", "c"})
	if err != nil {
		t.Fatalf("SetBackends : %v", err)
	}
	if len(upd.Backends) != 2 || upd.Backends[0] != "b" || upd.Backends[1] != "c" {
		t.Errorf("Backends after set = %v ; want [b,c]", upd.Backends)
	}
	// Caller's slice mutation must not leak through (defensive copy).
	caller := []string{"x", "y"}
	upd2, _ := s.SetBackends(ctx, "l1", caller)
	caller[0] = "MUTATED"
	if upd2.Backends[0] != "x" {
		t.Errorf("SetBackends aliased caller slice ; store now has %q", upd2.Backends[0])
	}
}

func TestMemory_SetBackendsMissing(t *testing.T) {
	s := NewMemory()
	_, err := s.SetBackends(context.Background(), "no-such", []string{"x"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetBackends missing = %v ; want ErrNotFound", err)
	}
}

func TestMemory_ToProtoRoundTrips(t *testing.T) {
	l := LoadBalancer{
		UUID: "l1", Name: "n", Mode: "L7", Address: "203.0.113.20", Port: 443,
		Backends: []string{"a", "b"}, AZ: "multi", Controller: "weft-network-dca",
		Project: "p", Status: "active", CreatedAtNs: 12345,
	}
	p := l.ToProto()
	if p.Name != l.Name || p.Mode != l.Mode || p.Address != l.Address || p.Port != l.Port ||
		len(p.Backends) != 2 || p.Az != l.AZ || p.Controller != l.Controller ||
		p.CreatedAtUnixNs != l.CreatedAtNs {
		t.Errorf("ToProto field mismatch : %+v", p)
	}
}
