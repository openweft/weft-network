package lifecycle

import (
	"context"
	"fmt"
	"log/slog"

	weftclient "github.com/openweft/weft-client"
	weftv1 "github.com/openweft/weft-proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openweft/weft-network/internal/store/router"
)

// agentClient is the minimal slice of weftv1.WeftAgentClient WeftClient
// needs. Defined locally so tests can satisfy it with a fake without
// pulling all 30+ RPCs from the full client interface. The real impl
// is *weftv1.weftAgentClient (the generated stub) — it implements
// the same three methods natively.
type agentClient interface {
	RegisterMicroVM(ctx context.Context, in *weftv1.RegisterMicroVMRequest, opts ...grpc.CallOption) (*weftv1.RegisterMicroVMResponse, error)
	StopVM(ctx context.Context, in *weftv1.StopVMRequest, opts ...grpc.CallOption) (*weftv1.StopVMResponse, error)
	DeleteVM(ctx context.Context, in *weftv1.DeleteVMRequest, opts ...grpc.CallOption) (*weftv1.DeleteVMResponse, error)
}

// WeftClient implements RouterLifecycle by calling the weft daemon's
// RegisterMicroVM RPC with the OCI image reference that ships
// weft-router itself. The image-mode handler on the weft side
// (cmd/weft/main.go, after weft commit integrating Prepare) handles
// the pull + share assembly + cmdline synthesis — so this client
// just passes (name, project, image) and trusts the server.
//
// Destroy is StopVM + DeleteVM, tolerating NotFound on either step
// (mirrors the "already gone" idempotence contract on
// RouterLifecycle.Destroy).
//
// For kind != egress or backend != gobgp, both Ensure and Destroy
// short-circuit — no weft-router micro-VM is associated, so the
// orchestrator stays out of the picture.
type WeftClient struct {
	log     *slog.Logger
	image   string // OCI image ref (e.g. ghcr.io/openweft/weft-router:v0.1.0)
	project string // weft project to spawn micro-VMs into ("platform" default)
	client  agentClient
	conn    *grpc.ClientConn
}

// NewWeftClient dials the weft daemon at socketPath (Unix socket) and
// returns a WeftClient ready to ensure / destroy weft-router
// micro-VMs. image is the OCI ref to spawn ; an empty image is
// rejected. project defaults to "platform" when empty.
//
// Caller owns Close — typically main calls it once at startup and
// defers Close at shutdown alongside the publisher.
func NewWeftClient(log *slog.Logger, socketPath, image, project string) (*WeftClient, error) {
	if image == "" {
		return nil, fmt.Errorf("weftclient: image is required")
	}
	if project == "" {
		project = "platform"
	}
	client, conn, err := weftclient.Client(socketPath)
	if err != nil {
		return nil, fmt.Errorf("weftclient: dial %q: %w", socketPath, err)
	}
	return &WeftClient{
		log:     log,
		image:   image,
		project: project,
		client:  client,
		conn:    conn,
	}, nil
}

// newWeftClientWithStub is the test seam — constructs a WeftClient
// around a pre-built agentClient (typically a fake) without dialing
// anything. Production callers use NewWeftClient.
func newWeftClientWithStub(log *slog.Logger, image, project string, c agentClient) *WeftClient {
	if project == "" {
		project = "platform"
	}
	return &WeftClient{log: log, image: image, project: project, client: c}
}

// Close drains the gRPC connection. Idempotent ; safe on a
// stub-built WeftClient where conn is nil.
func (w *WeftClient) Close() {
	if w.conn != nil {
		_ = w.conn.Close()
		w.conn = nil
	}
}

// Ensure spawns the matching weft-router micro-VM for r. Short-circuits
// for kind=peer or backend != gobgp (no micro-VM associated). The VM
// name is derived from the Router uuid so the same logical Router
// always maps to the same VM (Ensure-idempotency relies on weft's
// own "already exists" handling — currently surfaced as
// AlreadyExists / Internal ; we treat AlreadyExists as success).
func (w *WeftClient) Ensure(ctx context.Context, r router.Router) error {
	if r.Kind != "egress" || r.Backend != "gobgp" {
		return nil
	}
	name := vmNameFor(r.UUID)
	_, err := w.client.RegisterMicroVM(ctx, &weftv1.RegisterMicroVMRequest{
		Name:    name,
		Project: w.project,
		Image:   w.image,
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			// Idempotent — the VM already exists, that's fine.
			return nil
		}
		return fmt.Errorf("RegisterMicroVM %s: %w", name, err)
	}
	w.log.Info("router micro-VM registered", "router", r.UUID, "vm", name, "image", w.image)
	return nil
}

// Destroy tears down the matching micro-VM. Tolerates NotFound at
// each step — the orchestrator never errors on "already gone".
func (w *WeftClient) Destroy(ctx context.Context, uuid string) error {
	name := vmNameFor(uuid)
	if _, err := w.client.StopVM(ctx, &weftv1.StopVMRequest{Name: name, Project: w.project}); err != nil {
		if status.Code(err) != codes.NotFound {
			w.log.Warn("StopVM failed (continuing to DeleteVM)", "vm", name, "err", err)
		}
	}
	if _, err := w.client.DeleteVM(ctx, &weftv1.DeleteVMRequest{Name: name, Project: w.project}); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("DeleteVM %s: %w", name, err)
	}
	w.log.Info("router micro-VM destroyed", "router", uuid, "vm", name)
	return nil
}

// vmNameFor maps a Router uuid to the canonical weft VM name. Centralised
// so Ensure / Destroy / future status-fetch all agree on the mapping.
func vmNameFor(routerUUID string) string {
	return "weft-router-" + routerUUID
}
