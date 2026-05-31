package server

import (
	"context"
	"net"
	"testing"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestIntegration_GRPCEndToEnd spins up the real gRPC server in-process
// against a bufconn-style net.Listener (lo:0 with a random port) and
// exercises one mutation + one list per domain. Catches breakage
// that pure-Go unit tests miss : proto wire round-trips, server
// registration, the embedded UnimplementedNetworkControlPlaneServer
// not eating overrides, etc.
//
// All four domains drive the in-memory store ; we never hit etcd here.
// The etcd backend's contract is already covered by the same Store
// interface tests + manual smoke.
func TestIntegration_GRPCEndToEnd(t *testing.T) {
	// Listen on lo:0 — kernel picks a free port. Faster + more
	// realistic than bufconn (real socket, gRPC wire format).
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen : %v", err)
	}
	defer lis.Close()

	srv := grpc.NewServer()
	netv1.RegisterNetworkControlPlaneServer(srv, New(Options{}))
	go func() { _ = srv.Serve(lis) }()
	defer srv.GracefulStop()

	// Wait briefly for the server to come up before dialing.
	deadline := time.Now().Add(2 * time.Second)
	var conn *grpc.ClientConn
	for time.Now().Before(deadline) {
		conn, err = grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial : %v", err)
	}
	defer conn.Close()

	client := netv1.NewNetworkControlPlaneClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ---- scheduling rules : create + list + delete --------------
	cr, err := client.CreateSchedulingRule(ctx, &netv1.CreateSchedulingRuleRequest{
		Project: "p", Name: "rule-a", Count: 3,
	})
	if err != nil {
		t.Fatalf("CreateSchedulingRule : %v", err)
	}
	ls, err := client.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{Project: "p"})
	if err != nil {
		t.Fatalf("ListSchedulingRules : %v", err)
	}
	if len(ls.GetRules()) != 1 {
		t.Errorf("ListSchedulingRules len = %d ; want 1", len(ls.GetRules()))
	}
	if _, err := client.DeleteSchedulingRule(ctx, &netv1.DeleteSchedulingRuleRequest{Uuid: cr.GetRule().GetUuid()}); err != nil {
		t.Fatalf("DeleteSchedulingRule : %v", err)
	}

	// ---- DNS zones + records : create chain ---------------------
	cz, err := client.CreateDNSZone(ctx, &netv1.CreateDNSZoneRequest{
		Project: "p", Name: "weft.internal",
	})
	if err != nil {
		t.Fatalf("CreateDNSZone : %v", err)
	}
	if _, err := client.CreateDNSRecord(ctx, &netv1.CreateDNSRecordRequest{
		ZoneUuid: cz.GetZone().GetUuid(), Name: "alpha", Type: "A", Value: "10.0.0.1",
	}); err != nil {
		t.Fatalf("CreateDNSRecord : %v", err)
	}
	recs, _ := client.ListDNSRecords(ctx, &netv1.ListDNSRecordsRequest{ZoneUuid: cz.GetZone().GetUuid()})
	if len(recs.GetRecords()) != 1 {
		t.Errorf("ListDNSRecords len = %d ; want 1", len(recs.GetRecords()))
	}

	// ---- routers : create + list --------------------------------
	if _, err := client.CreateRouter(ctx, &netv1.CreateRouterRequest{
		Project: "p", Name: "peer-net-a-b", Kind: "peer", Networks: []string{"net-a", "net-b"},
	}); err != nil {
		t.Fatalf("CreateRouter : %v", err)
	}
	rs, _ := client.ListRouters(ctx, &netv1.ListRoutersRequest{Project: "p"})
	if len(rs.GetRouters()) != 1 {
		t.Errorf("ListRouters len = %d ; want 1", len(rs.GetRouters()))
	}

	// ---- load balancers : create + set-backends -----------------
	clb, err := client.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{
		Project: "p", Name: "web-prod", Mode: "L7", Port: 443, Backends: []string{"web-1"},
	})
	if err != nil {
		t.Fatalf("CreateLoadBalancer : %v", err)
	}
	upd, err := client.SetLoadBalancerBackends(ctx, &netv1.SetLoadBalancerBackendsRequest{
		Uuid: clb.GetLoadBalancer().GetUuid(), Backends: []string{"web-1", "web-2"},
	})
	if err != nil {
		t.Fatalf("SetLoadBalancerBackends : %v", err)
	}
	if len(upd.GetLoadBalancer().GetBackends()) != 2 {
		t.Errorf("backends after set = %d ; want 2", len(upd.GetLoadBalancer().GetBackends()))
	}

	// ---- error code propagation across the wire -----------------
	// A duplicate Create should surface as codes.AlreadyExists end to
	// end — verifies the status.Errorf inside the handlers makes it
	// past the gRPC marshalling intact.
	_, err = client.CreateLoadBalancer(ctx, &netv1.CreateLoadBalancerRequest{
		Project: "p", Name: "web-prod", Mode: "L7", Port: 443,
	})
	if err == nil {
		t.Fatal("duplicate Create should fail")
	}
	if st, _ := status.FromError(err); st.Code() != codes.AlreadyExists {
		t.Errorf("duplicate Create code = %s ; want AlreadyExists", st.Code())
	}
}
