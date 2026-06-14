package fips

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPoller_Seed_HydratesIndex(t *testing.T) {
	idx := New()
	src := func(context.Context) ([]SourceFIP, error) {
		return []SourceFIP{
			{UUID: "f1", Address: "203.0.113.10", NetworkUUID: "n1", Status: "active"},
			{UUID: "f2", Address: "203.0.113.11", NetworkUUID: "n1", Status: "available"},
			{UUID: "f3", Address: "203.0.113.12", NetworkUUID: "n2", Status: "active"},
		}, nil
	}
	p := NewPoller(idx, src, time.Hour, silentLog(), nil)
	if err := p.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got := idx.ActiveFIPsInNetworks([]string{"n1", "n2"})
	if len(got) != 2 {
		t.Fatalf("want 2 active addresses, got %d (%v)", len(got), got)
	}
}

func TestPoller_OnChangeFiresPerNetwork(t *testing.T) {
	idx := New()
	src := func(context.Context) ([]SourceFIP, error) {
		return []SourceFIP{
			{UUID: "f1", Address: "1.1.1.1", NetworkUUID: "n1", Status: "active"},
			{UUID: "f2", Address: "2.2.2.2", NetworkUUID: "n2", Status: "active"},
		}, nil
	}
	var mu sync.Mutex
	var fired []string
	p := NewPoller(idx, src, time.Hour, silentLog(), func(n string) {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, n)
	})
	if err := p.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 2 {
		t.Errorf("got %d onChange calls, want 2 : %v", len(fired), fired)
	}
}

func TestPoller_ReplaceAll_RemovedNetworkFiresOnce(t *testing.T) {
	idx := New()
	// Seed with one active FIP on n1.
	idx.Upsert(Entry{UUID: "f1", Address: "1.1.1.1", NetworkUUID: "n1", Mapped: true})
	// Source now reports n1 has lost the FIP (e.g. an unmap-and-release happened).
	src := func(context.Context) ([]SourceFIP, error) {
		return nil, nil
	}
	var fired []string
	p := NewPoller(idx, src, time.Hour, silentLog(), func(n string) { fired = append(fired, n) })
	if err := p.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(fired) != 1 || fired[0] != "n1" {
		t.Errorf("expected one onChange for n1, got %v", fired)
	}
	if got := idx.ActiveFIPsInNetworks([]string{"n1"}); len(got) != 0 {
		t.Errorf("expected n1 to be empty after removal, got %v", got)
	}
}

func TestPoller_ReplaceAll_ChurnedAddressFiresOnce(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", Address: "1.1.1.1", NetworkUUID: "n1", Mapped: true})
	// Same network, same uuid, new address — that's an upstream
	// reallocation. Cardinality is the same (1) but the address
	// changed, so the announce set is different.
	src := func(context.Context) ([]SourceFIP, error) {
		return []SourceFIP{
			{UUID: "f1", Address: "2.2.2.2", NetworkUUID: "n1", Status: "active"},
		}, nil
	}
	var fired []string
	p := NewPoller(idx, src, time.Hour, silentLog(), func(n string) { fired = append(fired, n) })
	if err := p.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(fired) != 1 {
		t.Errorf("churn must fire once, got %d : %v", len(fired), fired)
	}
}

func TestPoller_SourceErrorPropagatesFromSeed(t *testing.T) {
	idx := New()
	wantErr := errors.New("nats down")
	src := func(context.Context) ([]SourceFIP, error) { return nil, wantErr }
	p := NewPoller(idx, src, time.Hour, silentLog(), nil)
	if err := p.Seed(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("Seed err = %v, want %v", err, wantErr)
	}
}

func TestPoller_Run_TickRefreshes(t *testing.T) {
	idx := New()
	var calls int32
	var mu sync.Mutex
	src := func(context.Context) ([]SourceFIP, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return []SourceFIP{
			{UUID: "f1", Address: "1.1.1.1", NetworkUUID: "n1", Status: "active"},
		}, nil
	}
	p := NewPoller(idx, src, 30*time.Millisecond, silentLog(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	// Wait for at least 2 ticks (seed + 1 refresh) then cancel.
	time.Sleep(120 * time.Millisecond)
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Errorf("expected ≥ 2 calls (seed + 1 tick), got %d", calls)
	}
}
