package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	weftv1 "github.com/openweft/weft-proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openweft/weft-network/internal/store/router"
)

type fakeAgent struct {
	mu            sync.Mutex
	registerCalls []*weftv1.RegisterMicroVMRequest
	startCalls    []*weftv1.StartVMRequest
	stopCalls     []*weftv1.StopVMRequest
	deleteCalls   []*weftv1.DeleteVMRequest
	registerErr   error
	startErr      error
	stopErr       error
	deleteErr     error
}

func (f *fakeAgent) RegisterMicroVM(_ context.Context, in *weftv1.RegisterMicroVMRequest, _ ...grpc.CallOption) (*weftv1.RegisterMicroVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls = append(f.registerCalls, in)
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &weftv1.RegisterMicroVMResponse{}, nil
}

func (f *fakeAgent) StartVM(_ context.Context, in *weftv1.StartVMRequest, _ ...grpc.CallOption) (*weftv1.StartVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, in)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &weftv1.StartVMResponse{}, nil
}

func (f *fakeAgent) StopVM(_ context.Context, in *weftv1.StopVMRequest, _ ...grpc.CallOption) (*weftv1.StopVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, in)
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return &weftv1.StopVMResponse{}, nil
}

func (f *fakeAgent) DeleteVM(_ context.Context, in *weftv1.DeleteVMRequest, _ ...grpc.CallOption) (*weftv1.DeleteVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, in)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &weftv1.DeleteVMResponse{}, nil
}

func newWeftClientForTest(image, project string, c agentClient) *WeftClient {
	// slog handler that discards output keeps the test noise-free.
	return newWeftClientWithStub(slog.New(slog.NewTextHandler(io.Discard, nil)), image, project, c)
}

func TestEnsure_GobgpEgress_RegistersAndStarts(t *testing.T) {
	fa := &fakeAgent{}
	w := newWeftClientForTest("ghcr.io/openweft/weft-router:v0.1.0", "platform", fa)
	r := router.Router{UUID: "rt-1", Kind: "egress", Backend: "gobgp", External: "65000:198.51.100.1"}
	if err := w.Ensure(context.Background(), r); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.registerCalls) != 1 {
		t.Fatalf("Register count = %d, want 1", len(fa.registerCalls))
	}
	got := fa.registerCalls[0]
	if got.Name != "weft-router-rt-1" {
		t.Errorf("Name = %q, want weft-router-rt-1", got.Name)
	}
	if got.Image != "ghcr.io/openweft/weft-router:v0.1.0" {
		t.Errorf("Image = %q", got.Image)
	}
	if got.Project != "platform" {
		t.Errorf("Project = %q", got.Project)
	}
	// Without StartVM the VM is registered but never boots ; weft-router
	// stays cold, the subscriber never connects. Lock that in.
	if len(fa.startCalls) != 1 {
		t.Fatalf("Start count = %d, want 1", len(fa.startCalls))
	}
	if fa.startCalls[0].Name != "weft-router-rt-1" || fa.startCalls[0].Project != "platform" {
		t.Errorf("Start req shape wrong: %+v", fa.startCalls[0])
	}
}

func TestEnsure_StartIdempotenceOnAlreadyRunning(t *testing.T) {
	// weft surfaces "VM already running" via FailedPrecondition on some
	// drivers and AlreadyExists on others. Both should be no-ops so a
	// re-Ensure (eg. ResyncRouters at startup) doesn't error on a still-
	// running router.
	for _, code := range []codes.Code{codes.AlreadyExists, codes.FailedPrecondition} {
		t.Run(code.String(), func(t *testing.T) {
			fa := &fakeAgent{startErr: status.Error(code, "already running")}
			w := newWeftClientForTest("img", "p", fa)
			r := router.Router{UUID: "r1", Kind: "egress", Backend: "gobgp"}
			if err := w.Ensure(context.Background(), r); err != nil {
				t.Errorf("Ensure should swallow %s, got %v", code, err)
			}
		})
	}
}

func TestEnsure_StartOtherErrorPropagates(t *testing.T) {
	fa := &fakeAgent{startErr: status.Error(codes.Internal, "boom")}
	w := newWeftClientForTest("img", "p", fa)
	r := router.Router{UUID: "r1", Kind: "egress", Backend: "gobgp"}
	if err := w.Ensure(context.Background(), r); err == nil {
		t.Error("expected Start error to propagate")
	}
}

func TestEnsure_SkipsNonGobgpAndPeer(t *testing.T) {
	fa := &fakeAgent{}
	w := newWeftClientForTest("img", "platform", fa)
	cases := []router.Router{
		{UUID: "r1", Kind: "peer", Backend: "wireguard"},
		{UUID: "r2", Kind: "egress", Backend: "vyos"},
		{UUID: "r3", Kind: "egress", Backend: "frr"},
	}
	for _, r := range cases {
		if err := w.Ensure(context.Background(), r); err != nil {
			t.Errorf("Ensure(%s/%s): %v", r.Kind, r.Backend, err)
		}
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.registerCalls) != 0 {
		t.Errorf("expected zero RegisterMicroVM calls, got %d", len(fa.registerCalls))
	}
}

func TestEnsure_AlreadyExistsIsIdempotent(t *testing.T) {
	fa := &fakeAgent{registerErr: status.Error(codes.AlreadyExists, "vm exists")}
	w := newWeftClientForTest("img", "p", fa)
	r := router.Router{UUID: "r1", Kind: "egress", Backend: "gobgp"}
	if err := w.Ensure(context.Background(), r); err != nil {
		t.Errorf("Ensure should swallow AlreadyExists, got %v", err)
	}
}

func TestEnsure_OtherErrorsPropagate(t *testing.T) {
	fa := &fakeAgent{registerErr: status.Error(codes.Internal, "boom")}
	w := newWeftClientForTest("img", "p", fa)
	r := router.Router{UUID: "r1", Kind: "egress", Backend: "gobgp"}
	if err := w.Ensure(context.Background(), r); err == nil {
		t.Error("expected error to propagate")
	}
}

func TestDestroy_StopThenDelete(t *testing.T) {
	fa := &fakeAgent{}
	w := newWeftClientForTest("img", "p", fa)
	if err := w.Destroy(context.Background(), "r1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.stopCalls) != 1 || fa.stopCalls[0].Name != "weft-router-r1" {
		t.Errorf("stop calls = %v", fa.stopCalls)
	}
	if len(fa.deleteCalls) != 1 || fa.deleteCalls[0].Name != "weft-router-r1" {
		t.Errorf("delete calls = %v", fa.deleteCalls)
	}
}

func TestDestroy_NotFoundIsIdempotent(t *testing.T) {
	// Router was deleted while the controller was offline ; the
	// matching VM is already gone. Destroy should not error.
	fa := &fakeAgent{
		stopErr:   status.Error(codes.NotFound, "no such vm"),
		deleteErr: status.Error(codes.NotFound, "no such vm"),
	}
	w := newWeftClientForTest("img", "p", fa)
	if err := w.Destroy(context.Background(), "r1"); err != nil {
		t.Errorf("Destroy should swallow NotFound, got %v", err)
	}
}

func TestDestroy_DeleteOtherErrorPropagates(t *testing.T) {
	fa := &fakeAgent{deleteErr: errors.New("boom")}
	w := newWeftClientForTest("img", "p", fa)
	if err := w.Destroy(context.Background(), "r1"); err == nil {
		t.Error("expected DeleteVM error to propagate")
	}
}

func TestVmNameFor(t *testing.T) {
	if got := vmNameFor("abc-123"); got != "weft-router-abc-123" {
		t.Errorf("vmNameFor = %q", got)
	}
}
