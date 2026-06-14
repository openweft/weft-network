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
	prev := i.byUUID[e.UUID]
	if prev.UUID != "" {
		i.unindexLocked(prev)
	}
	i.indexLocked(e)
	touched := networksTouched(prev, e)
	i.refreshGaugeLocked(touched)
	i.mu.Unlock()
	return prev
}

// Delete removes the entry for uuid. Idempotent on absent.
func (i *Index) Delete(uuid string) Entry {
	i.mu.Lock()
	prev, ok := i.byUUID[uuid]
	if !ok {
		i.mu.Unlock()
		return Entry{}
	}
	i.unindexLocked(prev)
	i.refreshGaugeLocked(map[string]struct{}{prev.NetworkUUID: {}})
	i.mu.Unlock()
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

// ReplaceAll atomically swaps the index contents for the supplied
// snapshot. Returns three sets keyed by NetworkUUID identifying the
// nature of the change :
//
//   * added   — networks that gained at least one Mapped entry
//   * removed — networks that lost their last Mapped entry
//   * churned — networks where a Mapped entry's Address changed but
//               the count stayed > 0 (matters because the announce
//               set itself changed even if the cardinality didn't)
//
// Callers feed each affected network into their republish trigger
// so weft-router gets the updated /32 announce set on the next
// reconcile. Used by the Poller's tick + by ad-hoc seed paths.
func (i *Index) ReplaceAll(entries []Entry) (added, removed, churned map[string]struct{}) {
	added = make(map[string]struct{})
	removed = make(map[string]struct{})
	churned = make(map[string]struct{})

	i.mu.Lock()
	defer i.mu.Unlock()

	// Snapshot the prior per-network active address set.
	priorActive := make(map[string]map[string]struct{})
	for net, set := range i.netUUID {
		acc := make(map[string]struct{}, len(set))
		for uuid := range set {
			e := i.byUUID[uuid]
			if e.Mapped && e.Address != "" {
				acc[e.Address] = struct{}{}
			}
		}
		priorActive[net] = acc
	}

	// Rebuild from scratch.
	i.byUUID = make(map[string]Entry, len(entries))
	i.byAddr = make(map[string]string)
	i.netUUID = make(map[string]map[string]struct{})
	for _, e := range entries {
		i.indexLocked(e)
	}

	// Compute deltas per network.
	newActive := make(map[string]map[string]struct{})
	for net, set := range i.netUUID {
		acc := make(map[string]struct{}, len(set))
		for uuid := range set {
			e := i.byUUID[uuid]
			if e.Mapped && e.Address != "" {
				acc[e.Address] = struct{}{}
			}
		}
		newActive[net] = acc
	}

	// Union of all networks touched in either snapshot.
	allNets := make(map[string]struct{})
	for net := range priorActive {
		allNets[net] = struct{}{}
	}
	for net := range newActive {
		allNets[net] = struct{}{}
	}
	for net := range allNets {
		prior, hadPrior := priorActive[net]
		next, hasNext := newActive[net]
		if !hadPrior {
			prior = nil
		}
		if !hasNext {
			next = nil
		}
		switch {
		case len(prior) == 0 && len(next) > 0:
			added[net] = struct{}{}
		case len(prior) > 0 && len(next) == 0:
			removed[net] = struct{}{}
		case !sameAddrSet(prior, next):
			churned[net] = struct{}{}
		}
	}
	i.refreshGaugeLocked(allNets)
	return added, removed, churned
}

func sameAddrSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for addr := range a {
		if _, ok := b[addr]; !ok {
			return false
		}
	}
	return true
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

// networksTouched returns the union of the prior + next entry's
// NetworkUUID, used as the input to refreshGaugeLocked on Upsert.
// An entry that moves between networks (NetworkUUID flip) must
// rewrite the gauge for both labels.
func networksTouched(prev, next Entry) map[string]struct{} {
	out := make(map[string]struct{}, 2)
	if prev.NetworkUUID != "" {
		out[prev.NetworkUUID] = struct{}{}
	}
	if next.NetworkUUID != "" {
		out[next.NetworkUUID] = struct{}{}
	}
	return out
}

// refreshGaugeLocked recomputes the active-FIP count for every
// network in touched and writes it to indexEntries. Caller must hold
// i.mu (Upsert/Delete/ReplaceAll already do). Networks that drop to
// zero get an explicit 0 — important so a network whose last FIP was
// released doesn't keep reporting a stale non-zero value.
func (i *Index) refreshGaugeLocked(touched map[string]struct{}) {
	if len(touched) == 0 {
		return
	}
	ensureRegistered()
	for net := range touched {
		if net == "" {
			continue
		}
		var n int
		for uuid := range i.netUUID[net] {
			e := i.byUUID[uuid]
			if e.Mapped && e.Address != "" {
				n++
			}
		}
		indexEntries.WithLabelValues(net).Set(float64(n))
	}
}
