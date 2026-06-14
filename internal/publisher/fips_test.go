package publisher

import (
	"testing"

	"github.com/openweft/weft-network/internal/store/router"
)

// stubFIPLookup implements FIPLookup without dragging the fips
// package into this test file ; the round-trip through the real
// Index is covered by the fips package's own tests.
type stubFIPLookup struct {
	byNet map[string][]string
}

func (s stubFIPLookup) ActiveFIPsInNetworks(networks []string) []string {
	var out []string
	for _, n := range networks {
		out = append(out, s.byNet[n]...)
	}
	return out
}

func TestStateFor_AppendsActiveFIPsAsSingleHostCIDRs(t *testing.T) {
	r := router.Router{
		Kind: "egress", Backend: "gobgp", External: "65000:198.51.100.1",
		Networks: []string{"net-public", "net-tenant"},
		Prefixes: []string{"203.0.113.0/24"}, // operator-typed
	}
	fips := stubFIPLookup{byNet: map[string][]string{
		"net-public": {"203.0.113.42", "203.0.113.43"},
		"net-tenant": {"2001:db8::5"},
	}}

	state := StateFor(r, fips)

	// Operator prefix + 2 v4 /32 + 1 v6 /128 = 4 prefixes total.
	if len(state.Prefixes) != 4 {
		t.Fatalf("got %d prefixes, want 4 : %+v", len(state.Prefixes), state.Prefixes)
	}
	want := map[string]bool{
		"203.0.113.0/24":  true,
		"203.0.113.42/32": true,
		"203.0.113.43/32": true,
		"2001:db8::5/128": true,
	}
	for _, p := range state.Prefixes {
		if !want[p.Prefix] {
			t.Errorf("unexpected prefix %q", p.Prefix)
		}
		delete(want, p.Prefix)
	}
	if len(want) != 0 {
		t.Errorf("missing prefixes : %v", want)
	}
}

func TestStateFor_NoopFIPLookupBehavesLikeNil(t *testing.T) {
	r := router.Router{
		Kind: "egress", Backend: "gobgp", External: "65000:198.51.100.1",
		Networks: []string{"net-public"},
		Prefixes: []string{"203.0.113.0/24"},
	}
	withNoop := StateFor(r, NoopFIPLookup{})
	withNil := StateFor(r, nil)
	if len(withNoop.Prefixes) != 1 || len(withNil.Prefixes) != 1 {
		t.Fatalf("noop vs nil should be equivalent : noop=%d nil=%d",
			len(withNoop.Prefixes), len(withNil.Prefixes))
	}
}

func TestStateFor_SkipsUnparseableAddresses(t *testing.T) {
	r := router.Router{
		Kind: "egress", Backend: "gobgp", External: "65000:198.51.100.1",
		Networks: []string{"net-public"},
	}
	fips := stubFIPLookup{byNet: map[string][]string{
		"net-public": {"not-an-ip", "203.0.113.42", ""},
	}}
	state := StateFor(r, fips)
	if len(state.Prefixes) != 1 || state.Prefixes[0].Prefix != "203.0.113.42/32" {
		t.Errorf("unparseable should be skipped : %+v", state.Prefixes)
	}
}

func TestStateFor_FIPsOnPeerRouterIgnored(t *testing.T) {
	// kind=peer short-circuits before FIPs are considered — same
	// pre-existing contract.
	r := router.Router{Kind: "peer", Backend: "wireguard", Networks: []string{"net-public"}}
	fips := stubFIPLookup{byNet: map[string][]string{"net-public": {"203.0.113.42"}}}
	state := StateFor(r, fips)
	if len(state.Prefixes) != 0 {
		t.Errorf("peer router should not announce FIPs : %+v", state.Prefixes)
	}
}
