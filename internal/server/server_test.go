package server

import (
	"context"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
)

// All 16 RPCs are now wired (scheduling rules + DNS zones + DNS
// records + routers + load balancers). The earlier
// TestServer_StillUnimplemented table is gone — empty now would
// be dead code. Per-domain tests cover each RPC's contract.
//
// This smoke test guards the constructor : a default Server must
// be usable out of the box (memory stores), and at least one
// roundtrip (an empty List) must complete without error on every
// domain. A regression that broke Server.New ←→ store wiring
// shows up here even if no domain test panics.
func TestServer_DefaultsRoundTripEveryDomain(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"ListSchedulingRules": func() error {
			_, err := s.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{})
			return err
		},
		"ListDNSZones": func() error {
			_, err := s.ListDNSZones(ctx, &netv1.ListDNSZonesRequest{})
			return err
		},
		"ListDNSRecords": func() error {
			_, err := s.ListDNSRecords(ctx, &netv1.ListDNSRecordsRequest{})
			return err
		},
		"ListRouters": func() error {
			_, err := s.ListRouters(ctx, &netv1.ListRoutersRequest{})
			return err
		},
		"ListLoadBalancers": func() error {
			_, err := s.ListLoadBalancers(ctx, &netv1.ListLoadBalancersRequest{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err != nil {
				t.Errorf("%s on a fresh Server : %v", name, err)
			}
		})
	}
}
