package server

import (
	"context"
	"sync"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/publisher"
	"github.com/openweft/weft-network/internal/store/router"
)

// fakePublisher records every Publish / Withdraw call so a test can
// assert on them. Safe for concurrent use ; the Server tests don't
// race calls today but the abstraction allows it for free.
type fakePublisher struct {
	mu        sync.Mutex
	published []router.Router
	withdrawn []string
}

func (f *fakePublisher) Publish(_ context.Context, r router.Router) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, r)
	return nil
}

func (f *fakePublisher) Withdraw(_ context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = append(f.withdrawn, uuid)
	return nil
}

// compile-time conformance.
var _ publisher.RouterPublisher = (*fakePublisher)(nil)

func TestCreateRouter_CallsPublisher(t *testing.T) {
	fake := &fakePublisher{}
	s := New(Options{RouterPublisher: fake})
	ctx := context.Background()

	resp, err := s.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "p", Name: "egress-1", Kind: "egress", External: "65000:198.51.100.1",
	})
	if err != nil {
		t.Fatalf("CreateRouter: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.published) != 1 {
		t.Fatalf("publish count = %d, want 1", len(fake.published))
	}
	got := fake.published[0]
	if got.UUID != resp.GetRouter().GetUuid() {
		t.Errorf("published uuid = %q, want %q", got.UUID, resp.GetRouter().GetUuid())
	}
	if got.Kind != "egress" || got.Backend != "gobgp" {
		t.Errorf("published shape = {%s/%s}, want {egress/gobgp}", got.Kind, got.Backend)
	}
}

func TestDeleteRouter_CallsPublisherWithdraw(t *testing.T) {
	fake := &fakePublisher{}
	s := New(Options{RouterPublisher: fake})
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
	if len(fake.withdrawn) != 1 || fake.withdrawn[0] != uuid {
		t.Errorf("withdrawn = %v, want [%q]", fake.withdrawn, uuid)
	}
}

func TestResyncRouters_RepublishesAll(t *testing.T) {
	// Seed three routers via Create (the publisher records each). Then
	// reset the publisher's record and call ResyncRouters — it should
	// re-emit Publish for the same three (order doesn't matter ;
	// uuids do).
	fake := &fakePublisher{}
	s := New(Options{RouterPublisher: fake})
	ctx := context.Background()

	creates := []*netv1.CreateRouterRequest{
		{Project: "p", Name: "egress-a", Kind: "egress", External: "65000:198.51.100.1"},
		{Project: "p", Name: "egress-b", Kind: "egress", External: "65000:198.51.100.2"},
		{Project: "p", Name: "peer-c", Kind: "peer", Networks: []string{"n1", "n2"}},
	}
	wantUUIDs := map[string]bool{}
	for _, req := range creates {
		resp, err := s.CreateRouter(ctx, req)
		if err != nil {
			t.Fatalf("CreateRouter %s: %v", req.Name, err)
		}
		wantUUIDs[resp.GetRouter().GetUuid()] = true
	}

	// Reset and resync.
	fake.mu.Lock()
	fake.published = nil
	fake.mu.Unlock()

	n, err := s.ResyncRouters(ctx)
	if err != nil {
		t.Fatalf("ResyncRouters: %v", err)
	}
	if n != len(creates) {
		t.Errorf("ResyncRouters returned %d, want %d", n, len(creates))
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.published) != len(creates) {
		t.Fatalf("republish count = %d, want %d", len(fake.published), len(creates))
	}
	gotUUIDs := map[string]bool{}
	for _, r := range fake.published {
		gotUUIDs[r.UUID] = true
	}
	for u := range wantUUIDs {
		if !gotUUIDs[u] {
			t.Errorf("uuid %q not republished", u)
		}
	}
}

func TestServer_NilPublisherDefaultsToNoop(t *testing.T) {
	// When the operator doesn't wire a publisher, Server falls back to
	// publisher.Noop ; the CRUD still works, just logs at debug. This
	// is the dev-default path — without it, a test like
	// TestRouters_CreateListDelete would NPE on the publisher call.
	s := New(Options{})
	if _, err := s.CreateRouter(context.Background(), &netv1.CreateRouterRequest{
		Project: "p", Name: "egress-1", Kind: "egress", External: "198.51.100.1",
	}); err != nil {
		t.Fatalf("CreateRouter with nil RouterPublisher: %v", err)
	}
}
