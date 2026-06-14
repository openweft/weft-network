// metrics.go owns the Prometheus surface this package exposes for
// operator visibility into the floating-IP → BGP pipeline :
//
//   - weft_network_fips_index_entries{network}              — per-network active-FIP gauge
//   - weft_network_fips_subscriber_events_total{kind}       — Subscriber.handle event counter
//   - weft_network_fips_poller_ticks_total{why, result}     — Poller.tick counter
//   - weft_network_fips_poller_changes{kind}                — per-network change counter
//
// Operators alert on a stalled subscribe path with PromQL like :
//
//	rate(weft_network_fips_subscriber_events_total[5m]) == 0
//
// and on a flapping source with :
//
//	rate(weft_network_fips_poller_ticks_total{result="err"}[5m]) > 0
//
// Registration policy mirrors weft's firewallpub/metrics.go +
// floatingipnat/metrics.go : a package-level Register accepting a
// prometheus.Registerer scopes the collectors to a cmd-side Registry,
// otherwise the first instrumentation hit lazily binds them to
// prometheus.DefaultRegisterer so back-compat callers (tests, mini
// daemons) still produce values.
//
// Process-wide singletons : every Index / Subscriber / Poller in the
// binary shares the same collectors. Tests rely on the Write-style
// per-collector read (see metrics_test.go) and assert deltas around
// the call-under-test, never absolute values, so concurrent test
// ordering doesn't matter.

package fips

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// indexEntries is a gauge of active (Mapped) FIPs per network. The
	// Index sets it on every Upsert / Delete / ReplaceAll so the gauge
	// always tracks ActiveFIPsInNetworks(...) on the same network.
	// Networks that drop to zero have the gauge explicitly set to 0
	// rather than deleted ; the label set is bounded by the number of
	// networks that ever carried a FIP in this process lifetime, which
	// matches the cardinality of the rest of the network-keyed metrics.
	indexEntries = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "weft_network_fips_index_entries",
		Help: "Number of active (Mapped) floating-IP entries in the in-memory FIP index, labelled by network UUID. Tracks the cardinality of ActiveFIPsInNetworks for that network.",
	}, []string{"network"})

	// subscriberEventsTotal counts every event the Subscriber decodes,
	// labelled by the matched kind. The five label values cover the
	// four real kinds plus "unknown" for the silent-drop branch on
	// wildcard siblings the package doesn't react to ; a non-zero
	// unknown rate is the operator's signal that the upstream weft
	// emitter started publishing a new kind we should map.
	subscriberEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "weft_network_fips_subscriber_events_total",
		Help: "Total floating-IP platform events processed by the Subscriber, labelled by kind (allocated|released|mapped|unmapped|unknown). The unknown label covers wildcard siblings the package silently drops.",
	}, []string{"kind"})

	// pollerTicksTotal counts every Poller.tick invocation, labelled
	// by why (seed|refresh) and result (ok|err). seed is the initial
	// synchronous hydration in Run() ; refresh is the periodic tick.
	// The result label flips to err when SourceFn returns non-nil ;
	// the index is left untouched on err (matches Poller.tick contract).
	pollerTicksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "weft_network_fips_poller_ticks_total",
		Help: "Total Poller.tick invocations, labelled by why (seed|refresh) and result (ok|err). A failed tick leaves the index untouched ; the next tick recovers.",
	}, []string{"why", "result"})

	// pollerChanges counts per-network changes emitted by ReplaceAll,
	// labelled by kind (added|removed|churned). Used to spot upstream
	// churn that's invisible to operators reading only the gauge — a
	// FIP that flips address but stays mapped won't move the count
	// but will tick churned.
	pollerChanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "weft_network_fips_poller_changes",
		Help: "Per-network changes emitted by the Poller's ReplaceAll diff, labelled by kind (added|removed|churned). Counts networks, not entries.",
	}, []string{"kind"})

	// regOnce gates the one-shot Register call. Same semantics as
	// firewallpub's regOnce — first caller wins, subsequent calls are
	// no-ops (sync.Once contract).
	regOnce sync.Once
)

// Register binds the fips collectors to reg. Idempotent : only the
// first call has effect. Passing nil falls back to
// prometheus.DefaultRegisterer so back-compat callers (tests, bare
// daemon main()s) still produce values. Mirrors firewallpub.Register
// + floatingipnat.Register.
func Register(reg prometheus.Registerer) error {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	var err error
	regOnce.Do(func() {
		if e := reg.Register(indexEntries); e != nil {
			err = e
			return
		}
		if e := reg.Register(subscriberEventsTotal); e != nil {
			err = e
			return
		}
		if e := reg.Register(pollerTicksTotal); e != nil {
			err = e
			return
		}
		if e := reg.Register(pollerChanges); e != nil {
			err = e
			return
		}
	})
	return err
}

// ensureRegistered is the lazy fallback the instrumentation hot path
// calls before observing. If a cmd-side caller already invoked
// Register, the sync.Once is consumed and this is a no-op ; otherwise
// the collectors bind to DefaultRegisterer.
func ensureRegistered() {
	_ = Register(prometheus.DefaultRegisterer)
}
