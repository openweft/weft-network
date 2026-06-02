// Package lifecycle abstracts the orchestration that actually spawns
// and destroys the weft-router micro-VM behind a Router resource.
//
// weft-network is the Router controller : it persists desired-state in
// etcd, publishes BGP config on NATS, and observes status feedback from
// the data plane. What it does NOT do is talk to the host hypervisor
// to create the micro-VM that runs weft-router — that's an
// orchestration concern owned by whichever component knows how to
// reach weft-agent (or the weft control plane's API).
//
// This package defines the seam :
//
//   - RouterLifecycle.Ensure  : called after CreateRouter persists.
//     Implementation must produce a weft-router micro-VM with the
//     given Router's tenant_uuid as identity, listening on the
//     conventional NATS subjects.
//   - RouterLifecycle.Destroy : called after DeleteRouter persists.
//     Implementation tears the matching micro-VM down so the
//     dashboard's view eventually consists.
//
// The Noop default is what runs in dev mode (no orchestrator wired)
// and in tests : the Router resource persists, the NATS DesiredState
// is published, the dashboard reflects whatever weft-network sees —
// but no actual micro-VM gets spawned. Acceptable while the
// weft-router OCI image and the weft control-plane API are still
// stabilising ; an operator can hand-spawn the micro-VM with
// `weft microvm run ghcr.io/openweft/weft-router:vX.Y …` and it'll
// subscribe correctly because the contract is just the NATS subject.
//
// When the real implementation lands (separate package, calls the
// weft API), main wires it via server.Options.RouterLifecycle instead
// of the Noop default — same shape we already use for the publisher.
package lifecycle

import (
	"context"
	"log/slog"

	"github.com/openweft/weft-network/internal/store/router"
)

// RouterLifecycle is the seam weft-network's CRUD handlers go through.
// Implementations must be safe for concurrent calls.
//
// Idempotent on the contract :
//   - Ensure on a Router whose micro-VM already exists is a no-op (the
//     orchestrator is allowed to query and skip).
//   - Destroy on a uuid whose micro-VM is already gone is a no-op (the
//     orchestrator swallows "not found").
//
// Errors propagate up to the gRPC handler — but the publisher already
// landed the DesiredState on NATS, so a transient Ensure failure is
// recoverable : ResyncRouters at the next weft-network restart will
// re-attempt. Don't roll back the store on Ensure failure (mirrors
// the publisher's resilience contract).
type RouterLifecycle interface {
	Ensure(ctx context.Context, r router.Router) error
	Destroy(ctx context.Context, uuid string) error
}

// Noop is the default RouterLifecycle : logs intent at debug, returns
// nil. Used when no orchestrator is wired (dev) and in tests.
type Noop struct {
	Log *slog.Logger
}

// Ensure on Noop is a debug log + nil.
func (n Noop) Ensure(_ context.Context, r router.Router) error {
	if n.Log != nil {
		n.Log.Debug("noop router lifecycle ensure",
			"uuid", r.UUID, "kind", r.Kind, "backend", r.Backend)
	}
	return nil
}

// Destroy on Noop is a debug log + nil.
func (n Noop) Destroy(_ context.Context, uuid string) error {
	if n.Log != nil {
		n.Log.Debug("noop router lifecycle destroy", "uuid", uuid)
	}
	return nil
}
