package server

import (
	"context"
	"strings"
	"testing"

	netv1 "github.com/openweft/weft-network-proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSchedulingRules_CreateListDelete(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	cr, err := s.CreateSchedulingRule(ctx, &netv1.CreateSchedulingRuleRequest{
		Project:  "team-alpha",
		Name:     "nats-quorum",
		Count:    3,
		Selector: "app=nats",
		Az:       "different",
		Rack:     "any",
		Host:     "different",
	})
	if err != nil {
		t.Fatalf("CreateSchedulingRule : %v", err)
	}
	if cr.GetRule().GetUuid() == "" {
		t.Errorf("Create returned empty uuid")
	}
	if cr.GetRule().GetStatus() != "unschedulable" {
		t.Errorf("freshly-created rule status = %q ; want \"unschedulable\" (agent reports the real value)", cr.GetRule().GetStatus())
	}
	if cr.GetRule().GetCreatedAtUnixNs() == 0 {
		t.Errorf("Create didn't stamp created_at")
	}

	ls, err := s.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{Project: "team-alpha"})
	if err != nil {
		t.Fatalf("ListSchedulingRules : %v", err)
	}
	if len(ls.GetRules()) != 1 {
		t.Errorf("List(team-alpha) got %d rules ; want 1", len(ls.GetRules()))
	}

	// Project scoping : a rule for someone else shouldn't surface.
	if _, err := s.CreateSchedulingRule(ctx, &netv1.CreateSchedulingRuleRequest{
		Project: "team-bravo", Name: "etcd-quorum", Count: 3,
	}); err != nil {
		t.Fatalf("Create rule@bravo : %v", err)
	}
	ls, _ = s.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{Project: "team-alpha"})
	if len(ls.GetRules()) != 1 {
		t.Errorf("Scoping leak ; List(team-alpha) got %d ; want 1", len(ls.GetRules()))
	}
	// Empty project = all rules (admin view).
	all, _ := s.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{})
	if len(all.GetRules()) != 2 {
		t.Errorf("List(all) got %d ; want 2", len(all.GetRules()))
	}

	if _, err := s.DeleteSchedulingRule(ctx, &netv1.DeleteSchedulingRuleRequest{Uuid: cr.GetRule().GetUuid()}); err != nil {
		t.Fatalf("DeleteSchedulingRule : %v", err)
	}
	ls, _ = s.ListSchedulingRules(ctx, &netv1.ListSchedulingRulesRequest{Project: "team-alpha"})
	if len(ls.GetRules()) != 0 {
		t.Errorf("after delete, List(team-alpha) got %d ; want 0", len(ls.GetRules()))
	}
}

func TestSchedulingRules_CreateValidation(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()

	cases := []struct {
		name string
		req  *netv1.CreateSchedulingRuleRequest
		code codes.Code
	}{
		{"empty name", &netv1.CreateSchedulingRuleRequest{Count: 1}, codes.InvalidArgument},
		{"whitespace name", &netv1.CreateSchedulingRuleRequest{Name: "   ", Count: 1}, codes.InvalidArgument},
		{"zero count", &netv1.CreateSchedulingRuleRequest{Name: "r", Count: 0}, codes.InvalidArgument},
		{"negative count", &netv1.CreateSchedulingRuleRequest{Name: "r", Count: -1}, codes.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateSchedulingRule(ctx, c.req)
			if err == nil {
				t.Fatalf("expected error ; got nil")
			}
			st, _ := status.FromError(err)
			if st.Code() != c.code {
				t.Errorf("code = %s ; want %s", st.Code(), c.code)
			}
		})
	}
}

func TestSchedulingRules_DuplicateNameInProject(t *testing.T) {
	s := New(Options{})
	ctx := context.Background()
	r := &netv1.CreateSchedulingRuleRequest{Project: "p", Name: "dup", Count: 1}
	if _, err := s.CreateSchedulingRule(ctx, r); err != nil {
		t.Fatalf("first Create : %v", err)
	}
	_, err := s.CreateSchedulingRule(ctx, r)
	if err == nil {
		t.Fatal("expected AlreadyExists ; got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Errorf("code = %s ; want AlreadyExists", st.Code())
	}
	if !strings.Contains(st.Message(), "already exists") {
		t.Errorf("error message = %q ; want one mentioning 'already exists'", st.Message())
	}
}

func TestSchedulingRules_DeleteMissing(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteSchedulingRule(context.Background(), &netv1.DeleteSchedulingRuleRequest{Uuid: "no-such"})
	if err == nil {
		t.Fatal("expected NotFound ; got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Errorf("code = %s ; want NotFound", st.Code())
	}
}

func TestSchedulingRules_DeleteEmptyUUID(t *testing.T) {
	s := New(Options{})
	_, err := s.DeleteSchedulingRule(context.Background(), &netv1.DeleteSchedulingRuleRequest{Uuid: ""})
	if err == nil {
		t.Fatal("expected InvalidArgument ; got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %s ; want InvalidArgument", st.Code())
	}
}
