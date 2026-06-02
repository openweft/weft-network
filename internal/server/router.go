package server

import (
	"context"
	"errors"
	"strings"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store/router"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Kind / backend vocabularies. Reject early — the reconciler can't
// negotiate with a router pointing at a backend it doesn't speak.
//
// Backend choice for kind=egress :
//   - "gobgp" : default. Spawns a weft-router micro-VM running
//     GoBGP + netlink — pure-Go data plane, micro-VM-scaled, fits
//     the openweft microVM-first strategy.
//   - "vyos" / "frr" : escape hatch for tenants that need
//     multi-protocol routing (OSPF / IS-IS / RSVP-TE) or want to
//     bring their own router-OS config. Distributed as VM images,
//     run as classic VMs via `weft instance`, not micro-VMs.
//
// Peer routers (kind=peer) always use WireGuard ; no other backend fits.
var (
	validRouterKinds    = map[string]bool{"peer": true, "egress": true}
	validRouterBackends = map[string]bool{"wireguard": true, "gobgp": true, "vyos": true, "frr": true}
)

// ListRouters returns every router, optionally scoped to a project.
func (s *Server) ListRouters(ctx context.Context, req *netv1.ListRoutersRequest) (*netv1.ListRoutersResponse, error) {
	rs, err := s.stores.Routers.List(ctx, router.ListFilter{Project: req.GetProject()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list routers : %v", err)
	}
	out := make([]*netv1.RouterInfo, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ToProto())
	}
	return &netv1.ListRoutersResponse{Routers: out}, nil
}

// CreateRouter persists a new router. Kind drives the validation
// rules : peer routers need at least one network (the segments they
// stitch) ; egress routers need a non-empty External (peer AS / IP).
func (s *Server) CreateRouter(ctx context.Context, req *netv1.CreateRouterRequest) (*netv1.CreateRouterResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	kind := strings.TrimSpace(req.GetKind())
	if !validRouterKinds[kind] {
		return nil, status.Errorf(codes.InvalidArgument, "kind %q must be peer or egress", req.GetKind())
	}
	backend := strings.TrimSpace(req.GetBackend())
	if backend == "" {
		// Defaults per kind : peer → wireguard, egress → gobgp (the
		// micro-VM Go-native data plane via weft-router). Operators
		// override with backend="vyos" or "frr" only when they really
		// need the classic-VM escape hatch (multi-protocol routing, BYO
		// config). See the comment on validRouterBackends above.
		switch kind {
		case "peer":
			backend = "wireguard"
		case "egress":
			backend = "gobgp"
		}
	}
	if !validRouterBackends[backend] {
		return nil, status.Errorf(codes.InvalidArgument, "backend %q must be wireguard / gobgp / vyos / frr", req.GetBackend())
	}
	if kind == "peer" && len(req.GetNetworks()) < 1 {
		return nil, status.Error(codes.InvalidArgument, "peer routers must list at least one network")
	}
	if kind == "egress" && strings.TrimSpace(req.GetExternal()) == "" {
		return nil, status.Error(codes.InvalidArgument, "egress routers must declare external (AS / peer)")
	}
	r := router.Router{
		UUID:        newUUID(),
		Name:        name,
		Kind:        kind,
		Backend:     backend,
		Networks:    append([]string(nil), req.GetNetworks()...),
		External:    strings.TrimSpace(req.GetExternal()),
		Prefixes:    append([]string(nil), req.GetPrefixes()...),
		Project:     req.GetProject(),
		Status:      "configuring", // until the reconciler reports active
		CreatedAtNs: time.Now().UnixNano(),
	}
	saved, err := s.stores.Routers.Create(ctx, r)
	if err != nil {
		if errors.Is(err, router.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "router %q already exists in project %q", name, req.GetProject())
		}
		return nil, status.Errorf(codes.Internal, "create router : %v", err)
	}
	s.logger.Info("router created",
		"uuid", saved.UUID, "name", saved.Name, "kind", saved.Kind, "backend", saved.Backend)

	// Push desired-state to weft-router. For kind=peer and non-gobgp
	// egress, the publisher is a no-op (no weft-router micro-VM
	// subscribes anyway). Failure to publish is logged but doesn't
	// roll back the create — the store is the source of truth, the
	// next operator action (or a follow-up reconciler loop) re-tries.
	if err := s.publisher.Publish(ctx, saved); err != nil {
		s.logger.Warn("router publish failed (state in store but not on NATS)",
			"uuid", saved.UUID, "err", err)
	}
	// Ask the orchestrator to ensure a matching weft-router micro-VM
	// exists. Failure here is logged but not rolled back either : the
	// startup ResyncRouters re-attempts on the next weft-network boot.
	if err := s.lifecycle.Ensure(ctx, saved); err != nil {
		s.logger.Warn("router lifecycle ensure failed",
			"uuid", saved.UUID, "err", err)
	}
	return &netv1.CreateRouterResponse{Router: saved.ToProto()}, nil
}

// DeleteRouter removes a router by uuid.
func (s *Server) DeleteRouter(ctx context.Context, req *netv1.DeleteRouterRequest) (*netv1.DeleteRouterResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	if err := s.stores.Routers.Delete(ctx, uuid); err != nil {
		if errors.Is(err, router.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "router %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "delete router : %v", err)
	}
	s.logger.Info("router deleted", "uuid", uuid)
	// Withdraw the desired-state so a fresh weft-router micro-VM with
	// the same uuid doesn't re-apply the deleted state. Best-effort.
	if err := s.publisher.Withdraw(ctx, uuid); err != nil {
		s.logger.Warn("router withdraw failed", "uuid", uuid, "err", err)
	}
	// Tear down the matching micro-VM. Idempotent per the contract :
	// the orchestrator swallows "already gone".
	if err := s.lifecycle.Destroy(ctx, uuid); err != nil {
		s.logger.Warn("router lifecycle destroy failed", "uuid", uuid, "err", err)
	}
	return &netv1.DeleteRouterResponse{}, nil
}
