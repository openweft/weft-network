package server

import (
	"context"
	"sync"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/lifecycle"
	"github.com/openweft/weft-network/internal/store/router"
)

// fakeLifecycle records every Ensure / Destroy call. Mirrors the
// fakePublisher in publisher_wiring_test.go ; same contract, opposite
// orchestration concern.
type fakeLifecycle struct {
	mu       sync.Mutex
	ensured  []router.Router
	destroyed []string
}

func (f *fakeLifecycle) Ensure(_ context.Context, r router.Router) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, r)
	return nil
}

func (f *fakeLifecycle) Destroy(_ context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, uuid)
	return nil
}

var _ lifecycle.RouterLifecycle = (*fakeLifecycle)(nil)

func TestCreateRouter_CallsLifecycleEnsure(t *testing.T) {
	fake := &fakeLifecycle{}
	s := New(Options{RouterLifecycle: fake})
	ctx := context.Background()

	resp, err := s.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "p", Name: "egress-1", Kind: "egress", External: "65000:198.51.100.1",
	})
	if err != nil {
		t.Fatalf("CreateRouter: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.ensured) != 1 {
		t.Fatalf("ensured count = %d, want 1", len(fake.ensured))
	}
	if fake.ensured[0].UUID != resp.GetRouter().GetUuid() {
		t.Errorf("ensured uuid = %q, want %q",
			fake.ensured[0].UUID, resp.GetRouter().GetUuid())
	}
}

func TestDeleteRouter_CallsLifecycleDestroy(t *testing.T) {
	fake := &fakeLifecycle{}
	s := New(Options{RouterLifecycle: fake})
	ctx := context.Background()

	resp, err := s.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "p", Name: "egress-1", Kind: "egress", External: "65000:198.51.100.1",
	})
	if err != nil {
		t.Fatalf("CreateRouter: %v", err)
	}
	uuid := resp.GetRouter().GetUuid()

	if _, err := s.DeleteRouter(ctx, &netv1.DeleteRouterRequest{Uuid: uuid}); err != nil {
		t.Fatalf("DeleteRouter: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.destroyed) != 1 || fake.destroyed[0] != uuid {
		t.Errorf("destroyed = %v, want [%q]", fake.destroyed, uuid)
	}
}

func TestResyncRouters_CallsLifecycleEnsureForAll(t *testing.T) {
	fake := &fakeLifecycle{}
	s := New(Options{RouterLifecycle: fake})
	ctx := context.Background()

	wantUUIDs := map[string]bool{}
	for _, req := range []*netv1.CreateRouterRequest{
		{Project: "p", Name: "egress-a", Kind: "egress", External: "65000:198.51.100.1"},
		{Project: "p", Name: "egress-b", Kind: "egress", External: "65000:198.51.100.2"},
	} {
		resp, err := s.CreateRouter(ctx, req)
		if err != nil {
			t.Fatalf("CreateRouter %s: %v", req.Name, err)
		}
		wantUUIDs[resp.GetRouter().GetUuid()] = true
	}

	// Reset and resync.
	fake.mu.Lock()
	fake.ensured = nil
	fake.mu.Unlock()

	n, err := s.ResyncRouters(ctx)
	if err != nil {
		t.Fatalf("ResyncRouters: %v", err)
	}
	if n != len(wantUUIDs) {
		t.Errorf("ResyncRouters returned %d, want %d", n, len(wantUUIDs))
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.ensured) != len(wantUUIDs) {
		t.Fatalf("re-ensure count = %d, want %d", len(fake.ensured), len(wantUUIDs))
	}
	got := map[string]bool{}
	for _, r := range fake.ensured {
		got[r.UUID] = true
	}
	for u := range wantUUIDs {
		if !got[u] {
			t.Errorf("uuid %q not re-ensured", u)
		}
	}
}

func TestNilLifecycleDefaultsToNoop(t *testing.T) {
	// Options{} without an injected lifecycle still drives Create
	// through. The Noop fallback fires at debug, never panics.
	s := New(Options{})
	if _, err := s.CreateRouter(context.Background(), &netv1.CreateRouterRequest{
		Project: "p", Name: "egress-1", Kind: "egress", External: "198.51.100.1",
	}); err != nil {
		t.Fatalf("CreateRouter with nil RouterLifecycle: %v", err)
	}
}
