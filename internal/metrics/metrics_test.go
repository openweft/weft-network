package metrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNew_BuildInfoStamp(t *testing.T) {
	r := New("v1.2.3", "abc123", "2026-05-31T12:00:00Z")
	body := scrape(t, r)
	for _, want := range []string{
		`weft_network_build_info{commit="abc123",date="2026-05-31T12:00:00Z",version="v1.2.3"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q\n--- full body :\n%s", want, body)
		}
	}
}

func TestInterceptor_RecordsCallAndCode(t *testing.T) {
	r := New("dev", "none", "unknown")
	intr := r.UnaryInterceptor()

	info := &grpc.UnaryServerInfo{FullMethod: "/openweft.networkv1.NetworkControlPlane/ListRouters"}
	okHandler := func(_ context.Context, _ any) (any, error) { return "x", nil }
	if _, err := intr(context.Background(), nil, info, okHandler); err != nil {
		t.Fatalf("ok handler returned err : %v", err)
	}

	errHandler := func(_ context.Context, _ any) (any, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}
	if _, err := intr(context.Background(), nil, info, errHandler); !errors.Is(err, status.Error(codes.NotFound, "missing")) {
		// errors.Is doesn't match gRPC status wrappers ; we just want
		// to ensure the handler error is propagated, not equality.
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err handler propagation wrong : %v", err)
		}
	}

	body := scrape(t, r)
	// Both labels combinations recorded with count >= 1.
	for _, want := range []string{
		`weft_network_rpc_total{code="OK",method="/openweft.networkv1.NetworkControlPlane/ListRouters"} 1`,
		`weft_network_rpc_total{code="NotFound",method="/openweft.networkv1.NetworkControlPlane/ListRouters"} 1`,
		`weft_network_rpc_duration_seconds_count{code="OK",method="/openweft.networkv1.NetworkControlPlane/ListRouters"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}
}

func TestSetEtcdConnected(t *testing.T) {
	r := New("dev", "none", "unknown")
	if !strings.Contains(scrape(t, r), "weft_network_etcd_connected 0") {
		t.Error("etcd_connected should start at 0")
	}
	r.SetEtcdConnected(true)
	if !strings.Contains(scrape(t, r), "weft_network_etcd_connected 1") {
		t.Error("etcd_connected should flip to 1")
	}
}

// scrape exercises the /metrics handler in-process and returns the
// response body. Promhttp's text format is stable so substring
// assertions on label-ordered output are safe.
func scrape(t *testing.T, r *Recorder) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d ; want 200", rec.Code)
	}
	return rec.Body.String()
}
