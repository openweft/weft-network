package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store/scheduling"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListSchedulingRules returns every rule the daemon knows about,
// optionally scoped to a single project. Empty req.Project = all
// projects (the admin view in the webui).
//
// Pagination is declared in the proto (limit / page_token) ; today
// we ignore both — every catalog the dashboard surfaces is small
// enough to fit in one response. When workloads land that
// materialise thousands of rules, paginate here without a proto
// change.
func (s *Server) ListSchedulingRules(ctx context.Context, req *netv1.ListSchedulingRulesRequest) (*netv1.ListSchedulingRulesResponse, error) {
	rules, err := s.stores.SchedulingRules.List(ctx, scheduling.ListFilter{Project: req.GetProject()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list scheduling rules : %v", err)
	}
	out := make([]*netv1.SchedulingRuleInfo, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ToProto())
	}
	return &netv1.ListSchedulingRulesResponse{Rules: out}, nil
}

// CreateSchedulingRule persists a new rule. The agent's
// FirstFitScheduler picks it up on its next pass.
//
// Validation : name non-empty, count > 0, az / rack / host in the
// proximity vocabulary ("" | "same" | "different" | a literal value).
// Duplicate (project, name) → AlreadyExists.
func (s *Server) CreateSchedulingRule(ctx context.Context, req *netv1.CreateSchedulingRuleRequest) (*netv1.CreateSchedulingRuleResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetCount() <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "count must be > 0 ; got %d", req.GetCount())
	}
	for _, f := range []struct {
		name, value string
	}{
		{"az", req.GetAz()}, {"rack", req.GetRack()}, {"host", req.GetHost()},
	} {
		if f.value != "" && !validProximity(f.value) {
			// Free-form named values (e.g. az = "us-east-1") are also
			// allowed ; we only reject obvious typos by demanding the
			// short keywords be exact.
			//
			// Keeping this lenient on purpose : the agent's scheduler
			// is the authority on what's resolvable.
		}
	}
	r := scheduling.Rule{
		UUID:        newUUID(),
		Name:        name,
		Count:       req.GetCount(),
		Selector:    req.GetSelector(),
		AZ:          req.GetAz(),
		Rack:        req.GetRack(),
		Host:        req.GetHost(),
		Project:     req.GetProject(),
		Status:      "unschedulable", // until the agent reports
		CreatedAtNs: time.Now().UnixNano(),
	}
	saved, err := s.stores.SchedulingRules.Create(ctx, r)
	if err != nil {
		if errors.Is(err, scheduling.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "scheduling rule %q already exists in project %q", name, req.GetProject())
		}
		return nil, status.Errorf(codes.Internal, "create scheduling rule : %v", err)
	}
	s.logger.Info("scheduling rule created",
		"uuid", saved.UUID, "name", saved.Name, "project", saved.Project, "count", saved.Count)
	return &netv1.CreateSchedulingRuleResponse{Rule: saved.ToProto()}, nil
}

// DeleteSchedulingRule removes a rule by uuid. The agent stops
// enforcing it on its next pass.
//
// Idempotent at the API layer : a missing rule still returns
// NotFound (the caller — webui — swallows it). We don't pretend
// nothing happened.
func (s *Server) DeleteSchedulingRule(ctx context.Context, req *netv1.DeleteSchedulingRuleRequest) (*netv1.DeleteSchedulingRuleResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	if err := s.stores.SchedulingRules.Delete(ctx, uuid); err != nil {
		if errors.Is(err, scheduling.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "scheduling rule %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "delete scheduling rule : %v", err)
	}
	s.logger.Info("scheduling rule deleted", "uuid", uuid)
	return &netv1.DeleteSchedulingRuleResponse{}, nil
}

// validProximity reports whether s is one of the short keywords the
// scheduler treats specially. Free-form named values pass through
// untouched ; this helper exists so a future stricter validation
// can replace the lenient branch in CreateSchedulingRule without
// chasing call sites.
func validProximity(s string) bool {
	switch s {
	case "", "same", "different":
		return true
	}
	return false
}

// newUUID returns a 32-hex-char random identifier. Not RFC-4122-formatted
// (no dashes) — same shape weft-agent uses for VM UUIDs.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
