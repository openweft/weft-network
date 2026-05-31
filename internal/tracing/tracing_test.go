package tracing_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/openweft/weft-network/internal/tracing"
	"google.golang.org/grpc"
)

// TestInit_DisabledNoEndpoint exercises the empty-endpoint path : we
// expect a no-op shutdown that returns nil + no side effect that would
// stop us from re-Init'ing. The latter matters for tests that wire
// the daemon end-to-end multiple times in the same process.
func TestInit_DisabledNoEndpoint(t *testing.T) {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		shutdown, err := tracing.Init(ctx, tracing.Options{
			ServiceName: "weft-network-test",
			Version:     "v0.0.0",
		})
		if err != nil {
			t.Fatalf("iter %d : Init returned error : %v", i, err)
		}
		if shutdown == nil {
			t.Fatalf("iter %d : Init returned nil shutdown for disabled tracing", i)
		}
		if err := shutdown(ctx); err != nil {
			t.Fatalf("iter %d : noop shutdown returned error : %v", i, err)
		}
	}
}

// TestInit_OTLPEndpoint exercises the real wiring against a fake gRPC
// listener. The OTLP push doesn't have to actually deliver spans — the
// otlptracegrpc client is the trust boundary, we only verify Init
// builds the exporter + tracer provider without error and Shutdown
// completes.
func TestInit_OTLPEndpoint(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen : %v", err)
	}
	defer lis.Close()

	// Bare gRPC server : no services registered. Enough to satisfy
	// the OTLP client's connection handshake ; any subsequent Export
	// call gets Unimplemented but that's beyond what we test here.
	srv := grpc.NewServer()
	defer srv.Stop()
	go func() {
		_ = srv.Serve(lis)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdown, err := tracing.Init(ctx, tracing.Options{
		OTLPEndpoint: lis.Addr().String(),
		Insecure:     true,
		ServiceName:  "weft-network-test",
		Version:      "v0.0.0",
	})
	if err != nil {
		t.Fatalf("Init returned error : %v", err)
	}
	if shutdown == nil {
		t.Fatalf("Init returned nil shutdown")
	}

	// Re-install a no-op provider afterwards so other tests in the
	// same package run aren't observed through a half-torn-down SDK.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := shutdown(shutCtx); err != nil {
		t.Fatalf("shutdown returned error : %v", err)
	}

	// And one more no-op Init to confirm we can switch back to the
	// disabled path without panicking.
	noopShutdown, err := tracing.Init(context.Background(), tracing.Options{})
	if err != nil {
		t.Fatalf("post-shutdown noop Init returned error : %v", err)
	}
	_ = noopShutdown(context.Background())
}
