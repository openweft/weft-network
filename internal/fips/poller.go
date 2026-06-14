package fips

import (
	"context"
	"log/slog"
	"time"
)

// SourceFIP is the wire-shape SourceFn returns — minimal enough that
// the lifecycle.WeftClient adapter doesn't need to drag fips up the
// stack, but rich enough for the poller to make Mapped vs not-Mapped
// decisions identically to the subscriber.
//
// Status mirrors weft's FloatingIPInfo.status field ("active" |
// "available") ; the poller maps "active" → Mapped=true on the
// Index Entry.
type SourceFIP struct {
	UUID        string
	Address     string
	NetworkUUID string
	ProjectUUID string
	MappedTo    string
	Status      string
}

// SourceFn returns the current full FIP set from the upstream
// authority (weft). The poller calls this on every tick and the
// initial seed pass ; failure to reach the source logs + skips so a
// transient outage doesn't blank the index.
type SourceFn func(ctx context.Context) ([]SourceFIP, error)

// Poller seeds and refreshes an Index from a SourceFn. Two reasons
// it lives alongside the Subscriber rather than replacing it :
//
//  1. Startup seed : a freshly-restarted weft-network has no events
//     to replay (NATS is fire-and-forget) ; without an initial seed
//     the publisher would announce an empty FIP set until the next
//     mutation, briefly withdrawing every active /32 from upstream
//     BGP. The poller's first iteration runs synchronously in Run()
//     so the index is hydrated BEFORE the publisher's first call.
//  2. Drift safety net : if the Subscriber misses an event (network
//     blip, NATS server restart, decode bug), the next tick brings
//     the index back in line with weft. The Subscriber stays the
//     low-latency path ; the Poller is the eventual-consistency
//     guarantee.
//
// Reconcile is full-state replace : every tick rebuilds the entry
// set from the source and applies it via index.ReplaceAll. UUIDs
// removed upstream go away locally too.
type Poller struct {
	idx      *Index
	src      SourceFn
	every    time.Duration
	log      *slog.Logger
	onChange func(networkUUID string) // optional ; fires per network that gained or lost active FIPs
}

// NewPoller builds a Poller. every <= 0 defaults to 30 s. onChange
// may be nil ; the index still updates, but no republish is fired.
func NewPoller(idx *Index, src SourceFn, every time.Duration, log *slog.Logger, onChange func(string)) *Poller {
	if every <= 0 {
		every = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{idx: idx, src: src, every: every, log: log, onChange: onChange}
}

// Run hydrates the index once synchronously, then ticks every
// `every` until ctx is cancelled. Returns the seed error so the
// caller can decide whether to fail startup or proceed (typical
// production wiring : log + continue ; the next tick recovers).
func (p *Poller) Run(ctx context.Context) error {
	seedErr := p.tick(ctx, "seed")
	t := time.NewTicker(p.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return seedErr
		case <-t.C:
			_ = p.tick(ctx, "refresh")
		}
	}
}

// Seed runs one synchronous pass, returning the error if any.
// Exposed so a caller that prefers explicit-seed-then-Run can
// gate the Subscriber start on the seed succeeding.
func (p *Poller) Seed(ctx context.Context) error {
	return p.tick(ctx, "seed")
}

func (p *Poller) tick(ctx context.Context, why string) error {
	ensureRegistered()
	items, err := p.src(ctx)
	if err != nil {
		pollerTicksTotal.WithLabelValues(why, "err").Inc()
		p.log.Warn("fips poller: source fetch failed", "why", why, "err", err)
		return err
	}
	pollerTicksTotal.WithLabelValues(why, "ok").Inc()
	entries := make([]Entry, 0, len(items))
	for _, it := range items {
		entries = append(entries, Entry{
			UUID:        it.UUID,
			ProjectUUID: it.ProjectUUID,
			NetworkUUID: it.NetworkUUID,
			Address:     it.Address,
			Mapped:      it.Status == "active",
			Target:      it.MappedTo,
		})
	}
	added, removed, churned := p.idx.ReplaceAll(entries)
	pollerChanges.WithLabelValues("added").Add(float64(len(added)))
	pollerChanges.WithLabelValues("removed").Add(float64(len(removed)))
	pollerChanges.WithLabelValues("churned").Add(float64(len(churned)))
	if (len(added)+len(removed)+len(churned)) > 0 && p.onChange != nil {
		// Walk the union of affected networks and fire onChange.
		// Pass each network once — the callback typically does a
		// router list + republish, so dedup matters for cost.
		seen := make(map[string]struct{})
		for _, set := range []map[string]struct{}{added, removed, churned} {
			for n := range set {
				if _, dup := seen[n]; dup {
					continue
				}
				seen[n] = struct{}{}
				p.onChange(n)
			}
		}
	}
	p.log.Debug("fips poller tick",
		"why", why, "items", len(items),
		"added_nets", len(added), "removed_nets", len(removed), "churned_nets", len(churned))
	return nil
}
