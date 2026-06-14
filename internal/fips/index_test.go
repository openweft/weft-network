package fips

import (
	"sort"
	"testing"
)

func TestIndex_UpsertAndActiveLookup(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: true})
	idx.Upsert(Entry{UUID: "f2", NetworkUUID: "n1", Address: "203.0.113.11", Mapped: false}) // not mapped
	idx.Upsert(Entry{UUID: "f3", NetworkUUID: "n2", Address: "203.0.113.12", Mapped: true})  // wrong net

	got := idx.ActiveFIPsInNetworks([]string{"n1"})
	if len(got) != 1 || got[0] != "203.0.113.10" {
		t.Errorf("got %v, want [203.0.113.10]", got)
	}
}

func TestIndex_MultipleNetworksSorted(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.20", Mapped: true})
	idx.Upsert(Entry{UUID: "f2", NetworkUUID: "n2", Address: "203.0.113.10", Mapped: true})
	idx.Upsert(Entry{UUID: "f3", NetworkUUID: "n1", Address: "203.0.113.5", Mapped: true})

	got := idx.ActiveFIPsInNetworks([]string{"n1", "n2"})
	want := []string{"203.0.113.10", "203.0.113.20", "203.0.113.5"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d : %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestIndex_UpsertReplacesPriorState(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: true})

	// Replace with the same UUID but Mapped=false — should drop
	// from the active lookup.
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: false})

	if got := idx.ActiveFIPsInNetworks([]string{"n1"}); len(got) != 0 {
		t.Errorf("unmap should clear active : got %v", got)
	}
}

func TestIndex_DeleteIdempotent(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: true})

	prev := idx.Delete("f1")
	if prev.UUID != "f1" {
		t.Errorf("first delete should return the entry, got %+v", prev)
	}
	prev = idx.Delete("f1")
	if prev.UUID != "" {
		t.Errorf("second delete should return zero, got %+v", prev)
	}
}

func TestIndex_All(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f2", NetworkUUID: "n1", Address: "203.0.113.11", Mapped: true})
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: true})

	all := idx.All()
	if len(all) != 2 {
		t.Fatalf("All() = %d entries, want 2", len(all))
	}
	if all[0].UUID != "f1" || all[1].UUID != "f2" {
		t.Errorf("All() not sorted by UUID : %+v", all)
	}
}

func TestIndex_EmptyNetworksList(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{UUID: "f1", NetworkUUID: "n1", Address: "203.0.113.10", Mapped: true})
	if got := idx.ActiveFIPsInNetworks(nil); len(got) != 0 {
		t.Errorf("nil networks must return nil : %v", got)
	}
}
