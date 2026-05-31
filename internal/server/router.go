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
var (
	validRouterKinds    = map[string]bool{"peer": true, "egress": true}
	validRouterBackends = map[string]bool{"wireguard": true, "vyos": true, "frr": true}
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
		// Sensible default per kind : peer → wireguard, egress → vyos.
		// Operators override with backend="frr" for a Cisco-shop egress.
		switch kind {
		case "peer":
			backend = "wireguard"
		case "egress":
			backend = "vyos"
		}
	}
	if !validRouterBackends[backend] {
		return nil, status.Errorf(codes.InvalidArgument, "backend %q must be wireguard / vyos / frr", req.GetBackend())
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
	return &netv1.DeleteRouterResponse{}, nil
}
