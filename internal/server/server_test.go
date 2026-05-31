package server

import (
	"context"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RPCs still returning codes.Unimplemented are pinned here. As each
// gets wired, move its case out of this table and add coverage in
// the appropriate <domain>_test.go.
//
// Why this matters : the webui's live-first pattern only swaps from
// mock to live on Unimplemented. A future "I'll just return an empty
// response" shortcut would silently lie to the dashboard. This test
// catches that.
func TestServer_StillUnimplemented(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"ListLoadBalancers", func() error {
			_, err := s.ListLoadBalancers(ctx, &netv1.ListLoadBalancersRequest{})
			return err
		}},
		{"CreateLoadBalancer", func() error {
			_, err := s.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{})
			return err
		}},
		{"DeleteLoadBalancer", func() error {
			_, err := s.DeleteLoadBalancer(ctx, &netv1.DeleteLoadBalancerRequest{})
			return err
		}},
		{"SetLoadBalancerBackends", func() error {
			_, err := s.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{})
			return err
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if err == nil {
				t.Fatalf("%s returned nil error ; expected Unimplemented", c.name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s returned non-gRPC error : %v", c.name, err)
			}
			if st.Code() != codes.Unimplemented {
				t.Fatalf("%s returned code %s ; want Unimplemented", c.name, st.Code())
			}
		})
	}
}
