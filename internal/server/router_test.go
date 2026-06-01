package server

import (
	"context"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRouters_CreateListDelete(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	// peer router, default backend = wireguard.
	cr, err := s.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "platform", Name: "peer-a-b", Kind: "peer", Networks: []string{"net-a", "net-b"},
	})
	if err != nil {
		t.Fatalf("CreateRouter peer : %v", err)
	}
	if cr.GetRouter().GetBackend() != "wireguard" {
		t.Errorf("default backend for peer = %q ; want wireguard", cr.GetRouter().GetBackend())
	}
	if cr.GetRouter().GetStatus() != "configuring" {
		t.Errorf("fresh router status = %q ; want configuring", cr.GetRouter().GetStatus())
	}

	// egress router, default backend = gobgp (the weft-router micro-VM
	// path ; vyos/frr stay accepted as classic-VM escape hatches).
	er, err := s.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "platform", Name: "egress-prod", Kind: "egress", External: "65000:198.51.100.1",
	})
	if err != nil {
		t.Fatalf("CreateRouter egress : %v", err)
	}
	if er.GetRouter().GetBackend() != "gobgp" {
		t.Errorf("default backend for egress = %q ; want gobgp", er.GetRouter().GetBackend())
	}

	ls, _ := s.ListRouters(ctx, &netv1.ListRoutersRequest{Project: "platform"})
	if len(ls.GetRouters()) != 2 {
		t.Errorf("List(platform) = %d ; want 2", len(ls.GetRouters()))
	}

	if _, err := s.DeleteRouter(ctx, &netv1.DeleteRouterRequest{Uuid: cr.GetRouter().GetUuid()}); err != nil {
		t.Fatalf("DeleteRouter : %v", err)
	}
}

func TestRouters_CreateValidation(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	cases := []struct {
		name string
		req  *netv1.CreateRouterRequest
		code codes.Code
	}{
		{"empty name", &netv1.CreateRouterRequest{Kind: "peer"}, codes.InvalidArgument},
		{"bad kind", &netv1.CreateRouterRequest{Name: "r", Kind: "weird"}, codes.InvalidArgument},
		{"bad backend", &netv1.CreateRouterRequest{Name: "r", Kind: "peer", Backend: "envoy", Networks: []string{"n"}}, codes.InvalidArgument},
		{"peer needs networks", &netv1.CreateRouterRequest{Name: "r", Kind: "peer"}, codes.InvalidArgument},
		{"egress needs external", &netv1.CreateRouterRequest{Name: "r", Kind: "egress"}, codes.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateRouter(ctx, c.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if st, _ := status.FromError(err); st.Code() != c.code {
				t.Errorf("code = %s ; want %s", st.Code(), c.code)
			}
		})
	}
}

func TestRouters_DuplicateNameInProject(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	req := &netv1.CreateRouterRequest{Project: "p", Name: "dup", Kind: "peer", Networks: []string{"n"}}
	if _, err := s.CreateRouter(ctx, req); err != nil {
		t.Fatalf("first Create : %v", err)
	}
	_, err := s.CreateRouter(ctx, req)
	if st, _ := status.FromError(err); st.Code() != codes.AlreadyExists {
		t.Errorf("code = %s ; want AlreadyExists", st.Code())
	}
}

func TestRouters_DeleteMissing(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteRouter(context.Background(), &netv1.DeleteRouterRequest{Uuid: "no-such"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code = %s ; want NotFound", st.Code())
	}
}
