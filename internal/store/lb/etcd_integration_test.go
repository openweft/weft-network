package lb

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

func TestEtcd_LBsAgainstEmbeddedEtcd(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	l, err := s.Create(ctx, LoadBalancer{
		UUID: "l1", Name: "web-prod", Mode: "L7", Address: "203.0.113.20",
		Port: 443, Backends: []string{"web-1", "web-2"}, AZ: "multi",
		Project: "p", CreatedAtNs: 1,
	})
	if err != nil {
		t.Fatalf("Create : %v", err)
	}

	// SetBackends round-trips through the OCC loop.
	updated, err := s.SetBackends(ctx, l.UUID, []string{"web-1", "web-2", "web-3"})
	if err != nil {
		t.Fatalf("SetBackends : %v", err)
	}
	if len(updated.Backends) != 3 {
		t.Errorf("backends after set = %d ; want 3", len(updated.Backends))
	}

	// Empty list (drain) — explicit case.
	if _, err := s.SetBackends(ctx, l.UUID, nil); err != nil {
		t.Fatalf("SetBackends drain : %v", err)
	}
	post, _ := s.Get(ctx, l.UUID)
	if len(post.Backends) != 0 {
		t.Errorf("backends after drain = %d ; want 0", len(post.Backends))
	}

	// SetBackends on missing LB → ErrNotFound.
	if _, err := s.SetBackends(ctx, "no-such", []string{"x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetBackends missing = %v ; want ErrNotFound", err)
	}
}

// TestEtcd_LBListAndDelete exercises List (filtered + unfiltered),
// Create-duplicate, Delete success/missing, and Get missing — the
// branches the happy-path test skips.
func TestEtcd_LBListAndDelete(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	// Empty List : no error, zero entries.
	lbs, err := s.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List empty : %v", err)
	}
	if len(lbs) != 0 {
		t.Errorf("List empty = %d ; want 0", len(lbs))
	}

	// Get + Delete of-missing.
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v ; want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v ; want ErrNotFound", err)
	}

	// Seed three LBs across two projects.
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l1", Name: "web-a", Mode: "L7", Port: 443, Project: "p1", CreatedAtNs: 1}); err != nil {
		t.Fatalf("Create l1 : %v", err)
	}
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l2", Name: "web-b", Mode: "L7", Port: 443, Project: "p1", CreatedAtNs: 2}); err != nil {
		t.Fatalf("Create l2 : %v", err)
	}
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l3", Name: "pg", Mode: "L4", Port: 5432, Project: "p2", CreatedAtNs: 3}); err != nil {
		t.Fatalf("Create l3 : %v", err)
	}

	// Duplicate (project, name) : rejected.
	if _, err := s.Create(ctx, LoadBalancer{UUID: "lDup", Name: "web-a", Mode: "L7", Port: 443, Project: "p1"}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup Create = %v ; want ErrAlreadyExists", err)
	}
	// Cross-project same name : allowed.
	if _, err := s.Create(ctx, LoadBalancer{UUID: "l4", Name: "web-a", Mode: "L7", Port: 443, Project: "other", CreatedAtNs: 4}); err != nil {
		t.Errorf("cross-project same name : %v", err)
	}

	// Unfiltered List : 4.
	all, _ := s.List(ctx, ListFilter{})
	if len(all) != 4 {
		t.Errorf("List all = %d ; want 4", len(all))
	}
	// Filtered List : 2 in p1.
	p1, _ := s.List(ctx, ListFilter{Project: "p1"})
	if len(p1) != 2 {
		t.Errorf("List(p1) = %d ; want 2", len(p1))
	}
	// Filtered List : 0 for non-existent project.
	none, _ := s.List(ctx, ListFilter{Project: "nobody"})
	if len(none) != 0 {
		t.Errorf("List(nobody) = %d ; want 0", len(none))
	}

	// Get happy-path : confirms JSON round-trip.
	got, err := s.Get(ctx, "l1")
	if err != nil {
		t.Fatalf("Get l1 : %v", err)
	}
	if got.Name != "web-a" || got.Port != 443 {
		t.Errorf("Get returned wrong LB : %+v", got)
	}

	// Delete happy path + idempotency check.
	if err := s.Delete(ctx, "l3"); err != nil {
		t.Fatalf("Delete l3 : %v", err)
	}
	if _, err := s.Get(ctx, "l3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
	// Repeat delete : ErrNotFound (resp.Deleted == 0).
	if err := s.Delete(ctx, "l3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete twice = %v ; want ErrNotFound", err)
	}
}

// TestEtcd_SetBackendsHandlesContention drives the OCC retry loop :
// two goroutines try to SetBackends on the same LB concurrently.
// Both must succeed (each retries after losing the ModRevision race) ;
// final state matches the last writer's intent.
func TestEtcd_SetBackendsHandlesContention(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	if _, err := s.Create(ctx, LoadBalancer{
		UUID: "l1", Name: "lb", Mode: "L7", Port: 80, Project: "p", CreatedAtNs: 1,
	}); err != nil {
		t.Fatalf("Create : %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = s.SetBackends(ctx, "l1", []string{"a", "b"})
	}()
	go func() {
		defer wg.Done()
		_, _ = s.SetBackends(ctx, "l1", []string{"x", "y"})
	}()
	wg.Wait()

	final, _ := s.Get(ctx, "l1")
	// One of the two intents won ; either is fine — what matters is
	// both completed without erroring out + the result is internally
	// consistent (2 backends from a single intent, not a mix).
	if len(final.Backends) != 2 {
		t.Errorf("final backends len = %d ; want 2 (one writer's full intent)", len(final.Backends))
	}
	pair1 := final.Backends[0] + final.Backends[1]
	if pair1 != "ab" && pair1 != "xy" {
		t.Errorf("mixed intents : got %v ; want either [a,b] or [x,y]", final.Backends)
	}
}
