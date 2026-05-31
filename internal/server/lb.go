package server

import (
	"context"
	"errors"
	"strings"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store/lb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var validLBModes = map[string]bool{"L4": true, "L7": true}

// ListLoadBalancers returns every LB, optionally scoped to a project.
func (s *Server) ListLoadBalancers(ctx context.Context, req *netv1.ListLoadBalancersRequest) (*netv1.ListLoadBalancersResponse, error) {
	ls, err := s.stores.LBs.List(ctx, lb.ListFilter{Project: req.GetProject()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list load balancers : %v", err)
	}
	out := make([]*netv1.LoadBalancerInfo, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ToProto())
	}
	return &netv1.ListLoadBalancersResponse{LoadBalancers: out}, nil
}

// CreateLoadBalancer persists a new LB. Address (VIP) is allocated by
// the operator today — IPAM integration is a follow-on. The future
// reconciler programs each host's local Caddy with the resulting
// listener.
func (s *Server) CreateLoadBalancer(ctx context.Context, req *netv1.CreateLoadBalancerRequest) (*netv1.CreateLoadBalancerResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	mode := strings.ToUpper(strings.TrimSpace(req.GetMode()))
	if !validLBModes[mode] {
		return nil, status.Errorf(codes.InvalidArgument, "mode %q must be L4 or L7", req.GetMode())
	}
	if req.GetPort() == 0 || req.GetPort() > 65535 {
		return nil, status.Errorf(codes.InvalidArgument, "port must be 1-65535 ; got %d", req.GetPort())
	}
	az := strings.TrimSpace(req.GetAz())
	if az == "" {
		az = "multi"
	}
	l := lb.LoadBalancer{
		UUID:        newUUID(),
		Name:        name,
		Mode:        mode,
		Port:        req.GetPort(),
		Backends:    append([]string(nil), req.GetBackends()...),
		AZ:          az,
		Project:     req.GetProject(),
		Status:      "provisioning", // until the reconciler reports active
		CreatedAtNs: time.Now().UnixNano(),
	}
	saved, err := s.stores.LBs.Create(ctx, l)
	if err != nil {
		if errors.Is(err, lb.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "load balancer %q already exists in project %q", name, req.GetProject())
		}
		return nil, status.Errorf(codes.Internal, "create load balancer : %v", err)
	}
	s.logger.Info("load balancer created",
		"uuid", saved.UUID, "name", saved.Name, "mode", saved.Mode, "port", saved.Port,
		"az", saved.AZ, "backends", len(saved.Backends))
	return &netv1.CreateLoadBalancerResponse{LoadBalancer: saved.ToProto()}, nil
}

// DeleteLoadBalancer removes a load balancer by uuid.
func (s *Server) DeleteLoadBalancer(ctx context.Context, req *netv1.DeleteLoadBalancerRequest) (*netv1.DeleteLoadBalancerResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	if err := s.stores.LBs.Delete(ctx, uuid); err != nil {
		if errors.Is(err, lb.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "load balancer %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "delete load balancer : %v", err)
	}
	s.logger.Info("load balancer deleted", "uuid", uuid)
	return &netv1.DeleteLoadBalancerResponse{}, nil
}

// SetLoadBalancerBackends replaces the backend list atomically.
// Empty list is allowed (drains the LB) ; the reconciler then
// stops pointing traffic at any host.
func (s *Server) SetLoadBalancerBackends(ctx context.Context, req *netv1.SetLoadBalancerBackendsRequest) (*netv1.SetLoadBalancerBackendsResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	updated, err := s.stores.LBs.SetBackends(ctx, uuid, req.GetBackends())
	if err != nil {
		if errors.Is(err, lb.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "load balancer %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "set lb backends : %v", err)
	}
	s.logger.Info("load balancer backends updated",
		"uuid", uuid, "backends", len(updated.Backends))
	return &netv1.SetLoadBalancerBackendsResponse{LoadBalancer: updated.ToProto()}, nil
}
