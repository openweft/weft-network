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
// is the generated stub — it implements these four methods natively.
type agentClient interface {
	RegisterMicroVM(ctx context.Context, in *weftv1.RegisterMicroVMRequest, opts ...grpc.CallOption) (*weftv1.RegisterMicroVMResponse, error)
	StartVM(ctx context.Context, in *weftv1.StartVMRequest, opts ...grpc.CallOption) (*weftv1.StartVMResponse, error)
	StopVM(ctx context.Context, in *weftv1.StopVMRequest, opts ...grpc.CallOption) (*weftv1.StopVMResponse, error)
	DeleteVM(ctx context.Context, in *weftv1.DeleteVMRequest, opts ...grpc.CallOption) (*weftv1.DeleteVMResponse, error)
	ListFloatingIPs(ctx context.Context, in *weftv1.ListFloatingIPsRequest, opts ...grpc.CallOption) (*weftv1.ListFloatingIPsResponse, error)
}

// FIPSnapshot is the lifecycle-side projection of weft's
// FloatingIPInfo, used by the fips poller to seed + periodically
// refresh its index without dragging the weft-proto types up the
// call stack. Status is verbatim from weft : "active" when mapped,
// "available" when allocated-but-unmapped ; the fips package's
// adapter translates this into the Mapped boolean.
type FIPSnapshot struct {
	UUID        string
	Address     string
	NetworkUUID string
	ProjectUUID string
	MappedTo    string
	Status      string
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
// always maps to the same VM.
//
// Two-step : RegisterMicroVM materialises the inventory entry +
// pulls the image (the heavy lifting), StartVM actually boots the
// micro-VM. Skipping StartVM is the bug-shape "VM exists but never
// runs" — without it weft-router never boots, the NATS subscriber
// never connects, the DesiredState publisher emits into a void.
//
// Idempotence per RPC :
//   - RegisterMicroVM : AlreadyExists → success (re-Ensure on the
//     same router is a no-op past the inventory write).
//   - StartVM : we always issue it. weft's StartVM is itself
//     idempotent on an already-running VM (returns success), and we
//     treat AlreadyExists / FailedPrecondition as success too in case
//     a driver surfaces a different code.
func (w *WeftClient) Ensure(ctx context.Context, r router.Router) error {
	if r.Kind != "egress" || r.Backend != "gobgp" {
		return nil
	}
	names := vmNamesFor(r.UUID, r.Replicas)
	for _, name := range names {
		if _, err := w.client.RegisterMicroVM(ctx, &weftv1.RegisterMicroVMRequest{
			Name:    name,
			Project: w.project,
			Image:   w.image,
		}); err != nil && status.Code(err) != codes.AlreadyExists {
			return fmt.Errorf("RegisterMicroVM %s: %w", name, err)
		}

		if _, err := w.client.StartVM(ctx, &weftv1.StartVMRequest{
			Name:    name,
			Project: w.project,
		}); err != nil {
			switch status.Code(err) {
			case codes.AlreadyExists, codes.FailedPrecondition:
				// VM already running — fine.
			default:
				return fmt.Errorf("StartVM %s: %w", name, err)
			}
		}
		w.log.Info("router micro-VM ensured", "router", r.UUID, "vm", name, "image", w.image)
	}
	return nil
}

// Destroy tears down every matching micro-VM. The store-side
// RouterLifecycle.Destroy contract only carries the uuid, not the
// replica count — we probe both the legacy single-name layout
// ("weft-router-<uuid>") AND the multi-replica layout up to the
// orchestrator cap of 10. NotFound on either StopVM or DeleteVM
// is tolerated (idempotent : "already gone"), so a router that
// was created with replicas=1 cleans up in one round and the
// probe of higher indices is cheap (one NotFound RPC each).
func (w *WeftClient) Destroy(ctx context.Context, uuid string) error {
	// Try the legacy bare name first + every replicated suffix
	// up to maxReplicas. We don't know how many replicas were
	// configured at create time once the Router has been deleted
	// from the store ; the bounded probe is the simplest way to
	// avoid leaking microVMs.
	names := append([]string{vmBareName(uuid)}, replicaNames(uuid, maxReplicas)...)
	for _, name := range names {
		if _, err := w.client.StopVM(ctx, &weftv1.StopVMRequest{Name: name, Project: w.project}); err != nil {
			if status.Code(err) != codes.NotFound {
				w.log.Warn("StopVM failed (continuing to DeleteVM)", "vm", name, "err", err)
			}
		}
		if _, err := w.client.DeleteVM(ctx, &weftv1.DeleteVMRequest{Name: name, Project: w.project}); err != nil {
			if status.Code(err) == codes.NotFound {
				continue
			}
			return fmt.Errorf("DeleteVM %s: %w", name, err)
		}
		w.log.Info("router micro-VM destroyed", "router", uuid, "vm", name)
	}
	return nil
}

// ListFloatingIPs pulls every floating IP from weft (across all
// visible projects — admin auth in production). Used by the fips
// poller to seed its index at weft-network startup and as a safety
// net against missed NATS events. The empty Project on the request
// asks weft for the full visible set.
func (w *WeftClient) ListFloatingIPs(ctx context.Context) ([]FIPSnapshot, error) {
	resp, err := w.client.ListFloatingIPs(ctx, &weftv1.ListFloatingIPsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListFloatingIPs: %w", err)
	}
	if resp == nil {
		return nil, nil
	}
	out := make([]FIPSnapshot, 0, len(resp.FloatingIps))
	for _, f := range resp.FloatingIps {
		if f == nil {
			continue
		}
		out = append(out, FIPSnapshot{
			UUID:        f.Uuid,
			Address:     f.Address,
			NetworkUUID: f.Network,
			ProjectUUID: f.ProjectUuid,
			MappedTo:    f.MappedTo,
			Status:      f.Status,
		})
	}
	return out, nil
}

// maxReplicas caps the number of weft-router microVMs the
// orchestrator will spawn per Router. Matches the server-side
// validation in internal/server/router.go ; the probe loop in
// Destroy reuses this as the upper bound when tearing down a
// router whose replica count is no longer available.
const maxReplicas = 10

// vmBareName is the single-VM legacy layout : "weft-router-<uuid>".
// Returned by vmNamesFor when replicas == 1 so single-replica
// routers keep their pre-multi-replica name unchanged (operator
// workflows + status receiver subjects don't need to migrate).
func vmBareName(routerUUID string) string {
	return "weft-router-" + routerUUID
}

// vmNamesFor returns the deterministic VM names for the N replicas
// of one Router. replicas <= 1 collapses to the single legacy name
// ("weft-router-<uuid>") ; replicas >= 2 returns the indexed names
// ("weft-router-<uuid>-1", "-2", ...). All names sort
// lexicographically by index so the order is stable across calls.
func vmNamesFor(routerUUID string, replicas int) []string {
	if replicas <= 1 {
		return []string{vmBareName(routerUUID)}
	}
	if replicas > maxReplicas {
		replicas = maxReplicas
	}
	return replicaNames(routerUUID, replicas)
}

// replicaNames is the indexed-suffix slice ("-1", "-2", ...) used
// by both vmNamesFor (Ensure) and Destroy's probe loop.
func replicaNames(routerUUID string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = "weft-router-" + routerUUID + "-" + itoa(i+1)
	}
	return out
}

// itoa is a tiny strconv.Itoa replacement to keep the file
// import-light (this file already pulls fmt, but Itoa is the only
// caller we'd need strconv for).
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
