// Package fips maintains a NATS-fed index of every active floating IP
// in the platform so weft-network's RouterPublisher can announce them
// as /32 (or /128) BGP prefixes alongside the tenant's operator-typed
// prefix list.
//
// Architecture (per [[openweft-pull-model]] : cross-daemon = pull,
// not push) :
//
//   weft (control plane) ─ PlatformEvent on bus
//     "floating_ip.allocated|released|mapped|unmapped"
//                  │
//                  ▼ NATS subject "weft.events.floating_ip.*"
//   weft-network ── Subscriber.decode
//                  │
//                  ▼ Index.Update(ev) — in-memory map
//      (project, network) → set of active addresses
//                  │
//                  ▼ publisher.FIPLookup.ActiveFIPsInNetworks(r.Networks)
//                  ▼ DesiredState.Prefixes append <addr>/32 each
//                  ▼ NATS subject "weft.router.<uuid>.config"
//   weft-router (microVM) ── GoBGP AddPath /32 → upstream ISP
//
// The index is the only state — there's no Reconciler.Apply here.
// publisher.go reads the index synchronously on every Publish call ;
// the subscriber updates it asynchronously. Concurrent reads are
// guarded by sync.RWMutex.
//
// Status filtering : only events with status == "active" (i.e. a FIP
// that's mapped to a target) contribute an address. "available"
// allocations are tracked so we can promote them on the next mapped
// event without re-querying weft, but don't surface in the lookup.
package fips

import (
	"sort"
	"sync"
)

// Entry is the per-FIP record. Captured from the PlatformEvent Meta
// the weft adapter emits (network_uuid + address + target_kind +
// target). Kept minimal — the publisher only consumes Address, the
// rest is for diagnostics and the optional announce-list proto field.
type Entry struct {
	UUID        string
	ProjectUUID string
	NetworkUUID string
	Address     string
	// Mapped is true when the FIP has an active VM/LB target.
	// Unmapped (available) FIPs are tracked but don't surface in
	// ActiveFIPsInNetworks.
	Mapped bool
	// TargetKind and Target are the resolved kind+name a Map
	// operation bound this FIP to ; empty when Mapped is false.
	TargetKind string
	Target     string
}

// Index is the in-memory FIP table the subscriber feeds and the
// publisher reads.
type Index struct {
	mu      sync.RWMutex
	byUUID  map[string]Entry
	byAddr  map[string]string // (network, address) → uuid ; collision detection
	netUUID map[string]map[string]struct{} // network_uuid → set of FIP uuids
}

// New returns an empty Index.
func New() *Index {
	return &Index{
		byUUID:  make(map[string]Entry),
		byAddr:  make(map[string]string),
		netUUID: make(map[string]map[string]struct{}),
	}
}

// Upsert records or replaces e in the index, returning the previous
// value (or zero Entry when new). All secondary indexes are kept
// in lockstep — Mapped flips don't leak stale state.
func (i *Index) Upsert(e Entry) Entry {
	i.mu.Lock()
	defer i.mu.Unlock()
	prev := i.byUUID[e.UUID]
	if prev.UUID != "" {
		i.unindexLocked(prev)
	}
	i.indexLocked(e)
	return prev
}

// Delete removes the entry for uuid. Idempotent on absent.
func (i *Index) Delete(uuid string) Entry {
	i.mu.Lock()
	defer i.mu.Unlock()
	prev, ok := i.byUUID[uuid]
	if !ok {
		return Entry{}
	}
	i.unindexLocked(prev)
	return prev
}

// ActiveFIPsInNetworks returns the addresses of every Mapped FIP
// whose NetworkUUID is in the supplied list. Result is sorted to
// keep BGP UPDATE messages stable across reconciles — same input
// state → same byte payload.
//
// Implements publisher.FIPLookup ; production wiring assigns this
// method as the lookup so the publisher sees live data without
// pulling fips' import into publisher/.
func (i *Index) ActiveFIPsInNetworks(networks []string) []string {
	if len(networks) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(networks))
	for _, n := range networks {
		wanted[n] = struct{}{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	var out []string
	for net := range wanted {
		for uuid := range i.netUUID[net] {
			e := i.byUUID[uuid]
			if !e.Mapped || e.Address == "" {
				continue
			}
			out = append(out, e.Address)
		}
	}
	sort.Strings(out)
	return out
}

// All returns every entry in the index, sorted by UUID. Used by the
// poller for initial-state-seeding diff against the authoritative
// weft store, and by /metrics for the platform-wide gauge.
func (i *Index) All() []Entry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]Entry, 0, len(i.byUUID))
	for _, e := range i.byUUID {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UUID < out[b].UUID })
	return out
}

func (i *Index) indexLocked(e Entry) {
	i.byUUID[e.UUID] = e
	if e.Address != "" {
		i.byAddr[addrKey(e.NetworkUUID, e.Address)] = e.UUID
	}
	if e.NetworkUUID != "" {
		if _, ok := i.netUUID[e.NetworkUUID]; !ok {
			i.netUUID[e.NetworkUUID] = make(map[string]struct{})
		}
		i.netUUID[e.NetworkUUID][e.UUID] = struct{}{}
	}
}

func (i *Index) unindexLocked(e Entry) {
	delete(i.byUUID, e.UUID)
	if e.Address != "" {
		delete(i.byAddr, addrKey(e.NetworkUUID, e.Address))
	}
	if set, ok := i.netUUID[e.NetworkUUID]; ok {
		delete(set, e.UUID)
		if len(set) == 0 {
			delete(i.netUUID, e.NetworkUUID)
		}
	}
}

func addrKey(network, address string) string { return network + "\x00" + address }
