// Package publisher pushes router desired-state to the per-tenant NATS
// subject that weft-router micro-VMs subscribe to.
//
// Contract with weft-router :
//
//	subject : "weft.router.<router-uuid>.config" (NATS)
//	payload : JSON DesiredState — peers + prefixes
//
// weft-router's subscriber package (internal/subscriber) listens on
// the matching subject, decodes the same JSON, and calls bgp.ApplyPeers
// + bgp.ApplyPrefixes on its in-process GoBGP server. Every reconcile
// is idempotent on both sides : weft-network re-publishes whenever the
// Router resource changes, weft-router re-applies even when the message
// is a duplicate.
//
// The interface is intentionally tiny so the bulk of weft-network can
// be tested against a no-op publisher. The real NATS implementation
// lives in nats.go ; tests use Noop.
package publisher

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/openweft/weft-network/internal/store/router"
)

// PeerConfig mirrors weft-router/internal/bgp.PeerConfig — duplicated
// rather than imported because weft-network must not depend on
// weft-router's module graph (each side gets its own go.mod). The two
// types stay in lockstep by convention ; the JSON wire shape is the
// contract.
type PeerConfig struct {
	Address    string `json:"Address"`
	RemoteASN  uint32 `json:"RemoteASN"`
	HoldTime   uint32 `json:"HoldTime,omitempty"`
	PolicyName string `json:"PolicyName,omitempty"`
}

// PrefixAdvertisement mirrors weft-router/internal/bgp.PrefixAdvertisement.
type PrefixAdvertisement struct {
	Prefix      string   `json:"Prefix"`
	NextHop     string   `json:"NextHop,omitempty"`
	Communities []uint32 `json:"Communities,omitempty"`
}

// DesiredState is the JSON payload published on the per-router subject.
// Mirrors weft-router/internal/subscriber.DesiredState ; field names
// stay PascalCase to match Go's default JSON tags.
type DesiredState struct {
	Peers    []PeerConfig          `json:"peers"`
	Prefixes []PrefixAdvertisement `json:"prefixes"`
}

// RouterPublisher pushes the desired-state for one router and clears
// it when the router is deleted.
//
// Implementations must be safe for concurrent calls — weft-network's
// CreateRouter and DeleteRouter handlers may run interleaved.
type RouterPublisher interface {
	// Publish translates a Router into a DesiredState and sends it on
	// the matching NATS subject. Idempotent on the wire (NATS retains
	// the last message on the subject when JetStream is configured ;
	// without JetStream, the subscriber catches up on the next message).
	Publish(ctx context.Context, r router.Router) error

	// Withdraw clears the subject — used when a router is deleted so
	// a future weft-router micro-VM with the same uuid doesn't pick up
	// stale state. Errors logged but non-fatal.
	Withdraw(ctx context.Context, uuid string) error
}

// Noop is the default publisher : drops all calls, logs them at debug.
// Used in tests and when NATS isn't configured.
type Noop struct {
	Log *slog.Logger
}

// Publish on Noop logs the intent and returns nil.
func (n Noop) Publish(_ context.Context, r router.Router) error {
	if n.Log != nil {
		n.Log.Debug("noop router publish", "uuid", r.UUID, "kind", r.Kind, "backend", r.Backend)
	}
	return nil
}

// Withdraw on Noop logs the intent and returns nil.
func (n Noop) Withdraw(_ context.Context, uuid string) error {
	if n.Log != nil {
		n.Log.Debug("noop router withdraw", "uuid", uuid)
	}
	return nil
}

// SubjectFor returns the NATS subject for a router uuid — exported so
// weft-router's test harness can build the matching subject without
// pulling this package, and so weft-network's tests can assert on the
// subject before encoding.
func SubjectFor(uuid string) string {
	return "weft.router." + uuid + ".config"
}

// FIPLookup returns the active floating-IP addresses bound to VMs on
// the supplied tenant networks. The publisher consumes this on every
// Publish so the resulting BGP announce set tracks live mappings —
// when a tenant maps an FIP, weft-router learns about it on the next
// reconcile.
//
// Returned addresses are bare ("203.0.113.42") ; StateFor converts
// them to /32 (or /128 for IPv6) PrefixAdvertisements. nil → no FIPs
// (compatible with the Noop default).
type FIPLookup interface {
	ActiveFIPsInNetworks(networks []string) []string
}

// NoopFIPLookup returns the empty address list. Used in tests and
// before a real lookup is wired ; preserves backward-compatible
// "operator-typed Prefixes only" behaviour.
type NoopFIPLookup struct{}

// ActiveFIPsInNetworks returns nil — the platform isn't pushing
// dynamic FIP routes.
func (NoopFIPLookup) ActiveFIPsInNetworks([]string) []string { return nil }

// StateFor builds the DesiredState from a Router. For kind=peer routers
// (WireGuard, not BGP), returns an empty DesiredState — the WireGuard
// path is reconciled by a different surface. For kind=egress routers
// with backend != "gobgp" (the VyOS / FRR escape hatch), also empty :
// those are classic-VM-managed, not weft-router.
//
// FIPs : on top of the operator-supplied Prefixes, the function calls
// fips.ActiveFIPsInNetworks(r.Networks) and appends each active
// floating IP as a /32 (or /128 for v6) PrefixAdvertisement. Passing
// nil fips disables that path (same as NoopFIPLookup) — keeps the
// existing tests + scaffold callers compiling without a stub.
//
// External parsing : the proto's `External` field is a single string
// ; today we support either a bare peer IP ("198.51.100.1") or a
// colon-separated "<RemoteASN>:<peer-ip>" pair. The local ASN comes
// from the router's owning project ; for the scaffold we leave it 0
// and let weft-router complain — when the proto grows a structured
// External, this helper grows with it.
func StateFor(r router.Router, fips FIPLookup) DesiredState {
	if r.Kind != "egress" || r.Backend != "gobgp" {
		return DesiredState{}
	}
	peer, ok := parseExternalPeer(r.External)
	if !ok {
		return DesiredState{}
	}
	prefixes := make([]PrefixAdvertisement, 0, len(r.Prefixes))
	for _, cidr := range r.Prefixes {
		prefixes = append(prefixes, PrefixAdvertisement{Prefix: cidr})
	}
	if fips != nil {
		for _, addr := range fips.ActiveFIPsInNetworks(r.Networks) {
			if cidr := singleHostCIDR(addr); cidr != "" {
				prefixes = append(prefixes, PrefixAdvertisement{Prefix: cidr})
			}
		}
	}
	return DesiredState{
		Peers:    []PeerConfig{peer},
		Prefixes: prefixes,
	}
}

// singleHostCIDR turns a bare IP into the /32 (IPv4) or /128 (IPv6)
// CIDR a BGP UPDATE expects. Returns "" when ip can't be parsed —
// the caller skips the entry rather than poison the announce set
// with a malformed prefix.
func singleHostCIDR(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if addr.Is4() {
		return addr.String() + "/32"
	}
	return addr.String() + "/128"
}
