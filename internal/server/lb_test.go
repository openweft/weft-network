package server

import (
	"context"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLBs_CreateListDelete(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	cr, err := s.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{
		Project: "team-alpha", Name: "web-prod", Mode: "L7", Port: 443,
		Backends: []string{"web-1", "web-2"}, Az: "multi",
	})
	if err != nil {
		t.Fatalf("CreateLoadBalancer : %v", err)
	}
	got := cr.GetLoadBalancer()
	if got.GetStatus() != "provisioning" {
		t.Errorf("fresh LB status = %q ; want provisioning", got.GetStatus())
	}
	// Mode normalised to upper-case.
	cr2, _ := s.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{
		Project: "team-alpha", Name: "pg-rw", Mode: "l4", Port: 5432,
	})
	if cr2.GetLoadBalancer().GetMode() != "L4" {
		t.Errorf("mode not upper-cased ; got %q", cr2.GetLoadBalancer().GetMode())
	}
	// Default AZ = "multi" when empty.
	if cr2.GetLoadBalancer().GetAz() != "multi" {
		t.Errorf("default az = %q ; want multi", cr2.GetLoadBalancer().GetAz())
	}

	ls, _ := s.ListLoadBalancers(ctx, &netv1.ListLoadBalancersRequest{Project: "team-alpha"})
	if len(ls.GetLoadBalancers()) != 2 {
		t.Errorf("List(team-alpha) = %d ; want 2", len(ls.GetLoadBalancers()))
	}

	if _, err := s.DeleteLoadBalancer(ctx, &netv1.DeleteLoadBalancerRequest{Uuid: got.GetUuid()}); err != nil {
		t.Fatalf("DeleteLoadBalancer : %v", err)
	}
}

func TestLBs_CreateValidation(t *testing.T) {
	s := New(Options{})
	cases := []struct {
		name string
		req  *netv1.CreateLoadBalancerRequest
		code codes.Code
	}{
		{"empty name", &netv1.CreateLoadBalancerRequest{Mode: "L4", Port: 80}, codes.InvalidArgument},
		{"bad mode", &netv1.CreateLoadBalancerRequest{Name: "n", Mode: "L9", Port: 80}, codes.InvalidArgument},
		{"port 0", &netv1.CreateLoadBalancerRequest{Name: "n", Mode: "L4", Port: 0}, codes.InvalidArgument},
		{"port > 65535", &netv1.CreateLoadBalancerRequest{Name: "n", Mode: "L4", Port: 70000}, codes.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateLoadBalancer(context.Background(), c.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if st, _ := status.FromError(err); st.Code() != c.code {
				t.Errorf("code = %s ; want %s", st.Code(), c.code)
			}
		})
	}
}

func TestLBs_DuplicateNameInProject(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	req := &netv1.CreateLoadBalancerRequest{Project: "p", Name: "dup", Mode: "L4", Port: 80}
	if _, err := s.CreateLoadBalancer(ctx, req); err != nil {
		t.Fatalf("first Create : %v", err)
	}
	_, err := s.CreateLoadBalancer(ctx, req)
	if st, _ := status.FromError(err); st.Code() != codes.AlreadyExists {
		t.Errorf("code = %s ; want AlreadyExists", st.Code())
	}
}

func TestLBs_SetBackends(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	cr, _ := s.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{
		Project: "p", Name: "n", Mode: "L7", Port: 80, Backends: []string{"a"},
	})
	uuid := cr.GetLoadBalancer().GetUuid()

	upd, err := s.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{
		Uuid: uuid, Backends: []string{"b", "c"},
	})
	if err != nil {
		t.Fatalf("SetLoadBalancerBackends : %v", err)
	}
	if len(upd.GetLoadBalancer().GetBackends()) != 2 {
		t.Errorf("Backends after set = %d ; want 2", len(upd.GetLoadBalancer().GetBackends()))
	}

	// Empty list = drain. Allowed.
	upd, err = s.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{Uuid: uuid, Backends: nil})
	if err != nil {
		t.Fatalf("SetBackends drain : %v", err)
	}
	if len(upd.GetLoadBalancer().GetBackends()) != 0 {
		t.Errorf("Backends after drain = %d ; want 0", len(upd.GetLoadBalancer().GetBackends()))
	}

	// Missing LB.
	_, err = s.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{Uuid: "no-such", Backends: []string{"x"}})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("missing LB SetBackends code = %s ; want NotFound", st.Code())
	}

	// Empty uuid.
	_, err = s.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{Uuid: ""})
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Errorf("empty uuid SetBackends code = %s ; want InvalidArgument", st.Code())
	}
}

func TestLBs_DeleteMissing(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteLoadBalancer(context.Background(), &netv1.DeleteLoadBalancerRequest{Uuid: "no-such"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code = %s ; want NotFound", st.Code())
	}
}
