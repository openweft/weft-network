package scheduling

import (
	"context"
	"errors"
	"testing"
)

func TestMemory_CreateListDelete(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()

	a, err := s.Create(ctx, Rule{UUID: "u1", Name: "rule-a", Project: "team-alpha", Count: 3, CreatedAtNs: 1})
	if err != nil {
		t.Fatalf("Create rule-a : %v", err)
	}
	if a.UUID != "u1" {
		t.Errorf("Create returned wrong rule : %+v", a)
	}
	if _, err := s.Create(ctx, Rule{UUID: "u2", Name: "rule-b", Project: "team-alpha", CreatedAtNs: 2}); err != nil {
		t.Fatalf("Create rule-b : %v", err)
	}
	// Different project = no collision even with the same name.
	if _, err := s.Create(ctx, Rule{UUID: "u3", Name: "rule-a", Project: "team-bravo", CreatedAtNs: 3}); err != nil {
		t.Fatalf("Create rule-a@team-bravo : %v", err)
	}

	all, _ := s.List(ctx, ListFilter{})
	if len(all) != 3 {
		t.Errorf("List(all) got %d rules ; want 3", len(all))
	}
	// Ordering is created_at ascending — u1, u2, u3.
	if all[0].UUID != "u1" || all[1].UUID != "u2" || all[2].UUID != "u3" {
		t.Errorf("List(all) order = %v ; want [u1,u2,u3]", uuids(all))
	}

	scoped, _ := s.List(ctx, ListFilter{Project: "team-alpha"})
	if len(scoped) != 2 || scoped[0].UUID != "u1" || scoped[1].UUID != "u2" {
		t.Errorf("List(project=team-alpha) = %v ; want [u1,u2]", uuids(scoped))
	}

	if err := s.Delete(ctx, "u2"); err != nil {
		t.Fatalf("Delete u2 : %v", err)
	}
	if _, err := s.Get(ctx, "u2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
}

func TestMemory_CreateDuplicateInsideProject(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if _, err := s.Create(ctx, Rule{UUID: "u1", Name: "dup", Project: "p", CreatedAtNs: 1}); err != nil {
		t.Fatalf("Create : %v", err)
	}
	_, err := s.Create(ctx, Rule{UUID: "u2", Name: "dup", Project: "p", CreatedAtNs: 2})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate (project,name) = %v ; want ErrAlreadyExists", err)
	}
}

func TestMemory_DeleteMissing(t *testing.T) {
	s := NewMemory()
	err := s.Delete(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v ; want ErrNotFound", err)
	}
}

func TestMemory_ListEmptyIsNotError(t *testing.T) {
	s := NewMemory()
	rules, err := s.List(context.Background(), ListFilter{Project: "nobody"})
	if err != nil {
		t.Errorf("List empty = %v ; want nil", err)
	}
	if len(rules) != 0 {
		t.Errorf("List empty got %d rules ; want 0", len(rules))
	}
}

func TestMemory_ToProtoRoundTrips(t *testing.T) {
	r := Rule{
		UUID: "u1", Name: "n", Count: 3, Ready: 1, Selector: "tier=web",
		AZ: "different", Rack: "different", Host: "different",
		Project: "p", Status: "drifting", CreatedAtNs: 12345,
	}
	p := r.ToProto()
	if p.Uuid != r.UUID || p.Name != r.Name || p.Count != r.Count || p.Ready != r.Ready ||
		p.Selector != r.Selector || p.Az != r.AZ || p.Rack != r.Rack || p.Host != r.Host ||
		p.Project != r.Project || p.Status != r.Status || p.CreatedAtUnixNs != r.CreatedAtNs {
		t.Errorf("ToProto field mismatch ; got %+v ; want %+v", p, r)
	}
}

func uuids(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.UUID
	}
	return out
}
