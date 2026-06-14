package fips

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue + gaugeValue read the live value of the package-level
// collectors without going through /metrics. Tests are robust to
// other tests bumping the same collectors : assertions are written as
// deltas around the call-under-test, not absolute values. Mirrors the
// shape used in weft's firewallpub/metrics_test.go +
// floatingipnat/metrics_test.go.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.Counter.GetValue()
}

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("gauge.Write: %v", err)
	}
	return m.Gauge.GetValue()
}

func TestSubscriberEventsTotal_LabelsByKind(t *testing.T) {
	idx := New()
	sub := &Subscriber{idx: idx, log: silentLog()}

	allocBefore := counterValue(t, subscriberEventsTotal.WithLabelValues("allocated"))
	mapBefore := counterValue(t, subscriberEventsTotal.WithLabelValues("mapped"))
	unmapBefore := counterValue(t, subscriberEventsTotal.WithLabelValues("unmapped"))
	relBefore := counterValue(t, subscriberEventsTotal.WithLabelValues("released"))
	unkBefore := counterValue(t, subscriberEventsTotal.WithLabelValues("unknown"))

	sub.HandleMessage("weft.events.floating_ip.allocated",
		mkEvent("floating_ip.allocated", "fm-1", "proj-m", "net-m", "203.0.113.1", "", ""))
	sub.HandleMessage("weft.events.floating_ip.mapped",
		mkEvent("floating_ip.mapped", "fm-1", "proj-m", "net-m", "203.0.113.1", "vm", "web-1"))
	sub.HandleMessage("weft.events.floating_ip.unmapped",
		mkEvent("floating_ip.unmapped", "fm-1", "proj-m", "net-m", "203.0.113.1", "", ""))
	sub.HandleMessage("weft.events.floating_ip.released",
		mkEvent("floating_ip.released", "fm-1", "proj-m", "net-m", "203.0.113.1", "", ""))
	// Sibling on the wildcard the handler doesn't react to : must
	// tick the "unknown" label, not any of the four real kinds.
	sub.HandleMessage("weft.events.floating_ip.something_new",
		mkEvent("floating_ip.something_new", "fm-2", "proj-m", "net-m", "203.0.113.2", "", ""))

	if got := counterValue(t, subscriberEventsTotal.WithLabelValues("allocated")); got != allocBefore+1 {
		t.Errorf("subscriber_events_total{kind=allocated} = %v, want %v", got, allocBefore+1)
	}
	if got := counterValue(t, subscriberEventsTotal.WithLabelValues("mapped")); got != mapBefore+1 {
		t.Errorf("subscriber_events_total{kind=mapped} = %v, want %v", got, mapBefore+1)
	}
	if got := counterValue(t, subscriberEventsTotal.WithLabelValues("unmapped")); got != unmapBefore+1 {
		t.Errorf("subscriber_events_total{kind=unmapped} = %v, want %v", got, unmapBefore+1)
	}
	if got := counterValue(t, subscriberEventsTotal.WithLabelValues("released")); got != relBefore+1 {
		t.Errorf("subscriber_events_total{kind=released} = %v, want %v", got, relBefore+1)
	}
	if got := counterValue(t, subscriberEventsTotal.WithLabelValues("unknown")); got != unkBefore+1 {
		t.Errorf("subscriber_events_total{kind=unknown} = %v, want %v", got, unkBefore+1)
	}
}

func TestPollerTicksTotal_LabelsByWhyAndResult(t *testing.T) {
	idx := New()
	wantErr := errors.New("boom")
	calls := 0
	src := func(context.Context) ([]SourceFIP, error) {
		calls++
		if calls == 1 {
			return []SourceFIP{
				{UUID: "fm-tick-1", Address: "198.51.100.1", NetworkUUID: "net-tick", Status: "active"},
			}, nil
		}
		return nil, wantErr
	}
	p := NewPoller(idx, src, time.Hour, silentLog(), nil)

	seedOKBefore := counterValue(t, pollerTicksTotal.WithLabelValues("seed", "ok"))
	refreshErrBefore := counterValue(t, pollerTicksTotal.WithLabelValues("refresh", "err"))

	if err := p.tick(context.Background(), "seed"); err != nil {
		t.Fatalf("seed tick: %v", err)
	}
	if err := p.tick(context.Background(), "refresh"); !errors.Is(err, wantErr) {
		t.Fatalf("refresh tick err = %v, want %v", err, wantErr)
	}

	if got := counterValue(t, pollerTicksTotal.WithLabelValues("seed", "ok")); got != seedOKBefore+1 {
		t.Errorf("poller_ticks_total{why=seed,result=ok} = %v, want %v", got, seedOKBefore+1)
	}
	if got := counterValue(t, pollerTicksTotal.WithLabelValues("refresh", "err")); got != refreshErrBefore+1 {
		t.Errorf("poller_ticks_total{why=refresh,result=err} = %v, want %v", got, refreshErrBefore+1)
	}
}

func TestPollerChanges_LabelsByKind(t *testing.T) {
	idx := New()
	// Pre-seed n-removed with a Mapped entry — the upcoming tick
	// drops it (removed). n-added gets a brand-new entry (added).
	idx.Upsert(Entry{UUID: "fm-r", Address: "10.0.0.1", NetworkUUID: "n-removed", Mapped: true})
	// n-churned has a Mapped entry with addr X ; the tick replaces it
	// with the same UUID but a different address (churned).
	idx.Upsert(Entry{UUID: "fm-c", Address: "10.0.0.2", NetworkUUID: "n-churned", Mapped: true})

	src := func(context.Context) ([]SourceFIP, error) {
		return []SourceFIP{
			{UUID: "fm-a", Address: "10.0.0.10", NetworkUUID: "n-added", Status: "active"},
			{UUID: "fm-c", Address: "10.0.0.99", NetworkUUID: "n-churned", Status: "active"},
		}, nil
	}
	p := NewPoller(idx, src, time.Hour, silentLog(), nil)

	addedBefore := counterValue(t, pollerChanges.WithLabelValues("added"))
	removedBefore := counterValue(t, pollerChanges.WithLabelValues("removed"))
	churnedBefore := counterValue(t, pollerChanges.WithLabelValues("churned"))

	if err := p.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	if got := counterValue(t, pollerChanges.WithLabelValues("added")); got != addedBefore+1 {
		t.Errorf("poller_changes{kind=added} = %v, want %v", got, addedBefore+1)
	}
	if got := counterValue(t, pollerChanges.WithLabelValues("removed")); got != removedBefore+1 {
		t.Errorf("poller_changes{kind=removed} = %v, want %v", got, removedBefore+1)
	}
	if got := counterValue(t, pollerChanges.WithLabelValues("churned")); got != churnedBefore+1 {
		t.Errorf("poller_changes{kind=churned} = %v, want %v", got, churnedBefore+1)
	}
}

func TestIndexEntries_TracksUpsertDeleteAndReplaceAll(t *testing.T) {
	const network = "net-gauge-1"
	idx := New()

	// Two Mapped entries on the same network → gauge == 2.
	idx.Upsert(Entry{UUID: "g1", Address: "192.0.2.1", NetworkUUID: network, Mapped: true})
	idx.Upsert(Entry{UUID: "g2", Address: "192.0.2.2", NetworkUUID: network, Mapped: true})
	if got := gaugeValue(t, indexEntries.WithLabelValues(network)); got != 2 {
		t.Errorf("index_entries{network=%s} after 2 upserts = %v, want 2", network, got)
	}

	// Upserting g1 to Mapped=false (e.g. unmapped) drops the active
	// count to 1 — gauge must follow ActiveFIPsInNetworks, not the
	// raw row count.
	idx.Upsert(Entry{UUID: "g1", Address: "192.0.2.1", NetworkUUID: network, Mapped: false})
	if got := gaugeValue(t, indexEntries.WithLabelValues(network)); got != 1 {
		t.Errorf("index_entries{network=%s} after unmap = %v, want 1", network, got)
	}

	// Deleting the remaining Mapped entry → 0. Important : the
	// gauge must be explicitly set to 0, not left stale.
	idx.Delete("g2")
	if got := gaugeValue(t, indexEntries.WithLabelValues(network)); got != 0 {
		t.Errorf("index_entries{network=%s} after delete = %v, want 0", network, got)
	}

	// ReplaceAll refreshes too : seed two Mapped entries via the
	// full-state path and expect gauge == 2 again.
	idx.ReplaceAll([]Entry{
		{UUID: "g3", Address: "192.0.2.3", NetworkUUID: network, Mapped: true},
		{UUID: "g4", Address: "192.0.2.4", NetworkUUID: network, Mapped: true},
	})
	if got := gaugeValue(t, indexEntries.WithLabelValues(network)); got != 2 {
		t.Errorf("index_entries{network=%s} after ReplaceAll = %v, want 2", network, got)
	}
}

// TestRegister_AcceptsCustomRegisterer pins the back-compat contract :
// once the package-level Once has fired (any one of the tests above
// triggered ensureRegistered) Register returns nil regardless of the
// passed registerer. Mirrors the firewallpub + floatingipnat tests of
// the same shape.
func TestRegister_AcceptsCustomRegisterer(t *testing.T) {
	if err := Register(prometheus.NewRegistry()); err != nil {
		t.Errorf("Register on consumed Once should be a no-op, got %v", err)
	}
	if err := Register(nil); err != nil {
		t.Errorf("Register(nil) should fall back to default and no-op, got %v", err)
	}
}
