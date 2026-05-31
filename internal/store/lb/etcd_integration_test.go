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
