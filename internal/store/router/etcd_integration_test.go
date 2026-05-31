package router

import (
	"context"
	"errors"
	"testing"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

func TestEtcd_RoutersAgainstEmbeddedEtcd(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	if _, err := s.Create(ctx, Router{
		UUID: "r1", Name: "peer-a-b", Kind: "peer", Backend: "wireguard",
		Networks: []string{"net-a", "net-b"}, Project: "p", CreatedAtNs: 1,
	}); err != nil {
		t.Fatalf("Create peer : %v", err)
	}
	if _, err := s.Create(ctx, Router{
		UUID: "r2", Name: "egress-prod", Kind: "egress", Backend: "vyos",
		External: "AS65000", Project: "p", CreatedAtNs: 2,
	}); err != nil {
		t.Fatalf("Create egress : %v", err)
	}

	all, _ := s.List(ctx, ListFilter{})
	if len(all) != 2 {
		t.Errorf("List = %d ; want 2", len(all))
	}

	// Networks slice round-trips through etcd JSON.
	r1, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get : %v", err)
	}
	if len(r1.Networks) != 2 || r1.Networks[0] != "net-a" {
		t.Errorf("Networks round-trip = %v ; want [net-a, net-b]", r1.Networks)
	}

	// Duplicate (project, name) rejected.
	if _, err := s.Create(ctx, Router{
		UUID: "rX", Name: "peer-a-b", Kind: "peer", Project: "p", Networks: []string{"x"},
	}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup = %v ; want ErrAlreadyExists", err)
	}

	if err := s.Delete(ctx, "r1"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, err := s.Get(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
}
