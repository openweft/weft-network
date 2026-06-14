package fips

import (
	"encoding/json"
	"sync"
	"testing"
)

func mkEvent(kind, fipUUID, project, netUUID, addr, targetKind, target string) []byte {
	ev := platformEvent{
		Kind:        kind,
		Subject:     fipUUID,
		ProjectUUID: project,
		Meta: map[string]string{
			"network_uuid": netUUID,
			"address":      addr,
			"target_kind":  targetKind,
			"target":       target,
		},
	}
	b, _ := json.Marshal(ev)
	return b
}

func TestSubscriber_Handle_AllocatedThenMappedThenUnmappedThenReleased(t *testing.T) {
	idx := New()
	var changed []string
	var mu sync.Mutex
	sub := &Subscriber{idx: idx, log: silentLog(), onChange: func(net string) {
		mu.Lock()
		defer mu.Unlock()
		changed = append(changed, net)
	}}

	// 1. allocated → indexed but not Mapped, so absent from active lookup.
	sub.HandleMessage("weft.events.floating_ip.allocated",
		mkEvent("floating_ip.allocated", "f1", "proj-1", "net-1", "203.0.113.10", "", ""))
	if got := idx.ActiveFIPsInNetworks([]string{"net-1"}); len(got) != 0 {
		t.Errorf("allocated alone should not be active : %v", got)
	}
	if e, _ := lookupByUUID(idx, "f1"); e.UUID != "f1" || e.Mapped {
		t.Errorf("allocated entry = %+v", e)
	}

	// 2. mapped → surfaces in the lookup.
	sub.HandleMessage("weft.events.floating_ip.mapped",
		mkEvent("floating_ip.mapped", "f1", "proj-1", "net-1", "203.0.113.10", "vm", "web-1"))
	if got := idx.ActiveFIPsInNetworks([]string{"net-1"}); len(got) != 1 || got[0] != "203.0.113.10" {
		t.Errorf("mapped should activate : %v", got)
	}
	if e, _ := lookupByUUID(idx, "f1"); e.Target != "web-1" || e.TargetKind != "vm" {
		t.Errorf("mapped entry = %+v", e)
	}

	// 3. unmapped → out of the active lookup but still tracked.
	sub.HandleMessage("weft.events.floating_ip.unmapped",
		mkEvent("floating_ip.unmapped", "f1", "proj-1", "net-1", "203.0.113.10", "", ""))
	if got := idx.ActiveFIPsInNetworks([]string{"net-1"}); len(got) != 0 {
		t.Errorf("unmapped should drop from active : %v", got)
	}

	// 4. released → gone from the index entirely.
	sub.HandleMessage("weft.events.floating_ip.released",
		mkEvent("floating_ip.released", "f1", "proj-1", "net-1", "203.0.113.10", "", ""))
	if e, ok := lookupByUUID(idx, "f1"); ok {
		t.Errorf("released entry should be removed : %+v", e)
	}

	// onChange fired four times, all on net-1.
	mu.Lock()
	defer mu.Unlock()
	if len(changed) != 4 {
		t.Errorf("expected 4 onChange callbacks, got %d (%v)", len(changed), changed)
	}
	for i, n := range changed {
		if n != "net-1" {
			t.Errorf("changed[%d] = %s, want net-1", i, n)
		}
	}
}

func TestSubscriber_DropsMalformedJSON(t *testing.T) {
	idx := New()
	sub := &Subscriber{idx: idx, log: silentLog()}
	sub.HandleMessage("weft.events.floating_ip.mapped", []byte("{not json"))
	if len(idx.All()) != 0 {
		t.Errorf("malformed event must not pollute index")
	}
}

func TestSubscriber_IgnoresUnknownKind(t *testing.T) {
	idx := New()
	sub := &Subscriber{idx: idx, log: silentLog()}
	sub.HandleMessage("weft.events.floating_ip.renamed",
		mkEvent("floating_ip.renamed", "f1", "p", "n", "1.2.3.4", "", ""))
	if len(idx.All()) != 0 {
		t.Errorf("unknown kind must not touch index")
	}
}

// lookupByUUID is a tiny test helper — Index exposes only All() and
// ActiveFIPsInNetworks, so we need a side door to assert per-entry
// state without changing the public surface.
func lookupByUUID(idx *Index, uuid string) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	e, ok := idx.byUUID[uuid]
	return e, ok
}
