package router

import (
	"context"
	"errors"
	"testing"
)

func TestMemory_CRUD(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	r1, err := s.Create(ctx, Router{
		UUID: "r1", Name: "peer-a-b", Kind: "peer", Backend: "wireguard",
		Networks: []string{"net-a", "net-b"}, Project: "platform", CreatedAtNs: 1,
	})
	if err != nil {
		t.Fatalf("Create : %v", err)
	}
	if r1.UUID != "r1" {
		t.Errorf("returned wrong router : %+v", r1)
	}
	if _, err := s.Create(ctx, Router{UUID: "r2", Name: "egress-prod", Kind: "egress", Backend: "vyos", Project: "platform", CreatedAtNs: 2}); err != nil {
		t.Fatalf("Create r2 : %v", err)
	}
	all, _ := s.List(ctx, ListFilter{})
	if len(all) != 2 || all[0].UUID != "r1" || all[1].UUID != "r2" {
		t.Errorf("List(all) order wrong : %v", uuids(all))
	}
	if err := s.Delete(ctx, "r1"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, err := s.Get(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
}

func TestMemory_DuplicateInProject(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if _, err := s.Create(ctx, Router{UUID: "a", Name: "dup", Project: "p", CreatedAtNs: 1}); err != nil {
		t.Fatalf("Create : %v", err)
	}
	_, err := s.Create(ctx, Router{UUID: "b", Name: "dup", Project: "p", CreatedAtNs: 2})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup = %v ; want ErrAlreadyExists", err)
	}
}

func TestMemory_ToProtoRoundTrips(t *testing.T) {
	r := Router{
		UUID: "r1", Name: "n", Kind: "peer", Backend: "wireguard",
		Networks: []string{"a", "b"}, External: "AS65000", PeerState: "handshake 12s ago",
		Project: "p", Status: "active", CreatedAtNs: 12345,
	}
	p := r.ToProto()
	if p.Name != r.Name || p.Kind != r.Kind || p.Backend != r.Backend ||
		len(p.Networks) != 2 || p.Networks[0] != "a" || p.External != r.External ||
		p.PeerState != r.PeerState || p.CreatedAtUnixNs != r.CreatedAtNs {
		t.Errorf("ToProto field mismatch : %+v", p)
	}
}

func TestMemory_UpdateStatus(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	r := Router{UUID: "u1", Name: "r1", Kind: "egress", Backend: "gobgp", Project: "p", Status: "configuring"}
	if _, err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.UpdateStatus(ctx, "u1", "active", "203.0.113.1:Established ; routes=4"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "active" || got.PeerState != "203.0.113.1:Established ; routes=4" {
		t.Errorf("update not applied : status=%q peer_state=%q", got.Status, got.PeerState)
	}
	// Other fields preserved.
	if got.Name != "r1" || got.Backend != "gobgp" {
		t.Errorf("desired-state mutated : %+v", got)
	}
	// Unknown uuid → ErrNotFound.
	if err := s.UpdateStatus(ctx, "no-such", "active", ""); err != ErrNotFound {
		t.Errorf("UpdateStatus(missing) = %v, want ErrNotFound", err)
	}
}

func uuids(rs []Router) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.UUID
	}
	return out
}
