package scheduling

import (
	"context"
	"errors"
	"testing"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

// TestEtcd_CRUDAgainstEmbeddedEtcd hits the etcd backend
// end-to-end (no mocks) and verifies the Store interface contract
// holds. Mirrors the in-memory test suite ; catches bugs that only
// surface against a real etcd server (Txn semantics, prefix
// scans, sort order, …).
func TestEtcd_CRUDAgainstEmbeddedEtcd(t *testing.T) {
	c, _ := etcdtest.New(t)
	s := NewEtcd(c)
	ctx := context.Background()

	// Create + Get round-trip.
	r1, err := s.Create(ctx, Rule{
		UUID: "u1", Name: "rule-a", Project: "p", Count: 3, CreatedAtNs: 1,
	})
	if err != nil {
		t.Fatalf("Create : %v", err)
	}
	got, err := s.Get(ctx, r1.UUID)
	if err != nil || got.Name != "rule-a" {
		t.Errorf("Get u1 = %+v err=%v", got, err)
	}

	// Cross-project same-name : allowed.
	if _, err := s.Create(ctx, Rule{UUID: "u2", Name: "rule-a", Project: "other", CreatedAtNs: 2}); err != nil {
		t.Errorf("same name in other project should be allowed : %v", err)
	}

	// Duplicate (project, name) : rejected.
	if _, err := s.Create(ctx, Rule{UUID: "u3", Name: "rule-a", Project: "p", CreatedAtNs: 3}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate Create = %v ; want ErrAlreadyExists", err)
	}

	// List is sorted by createRevision (etcd's CreateRevision ≠ our
	// CreatedAtNs, but the test data was inserted in that order so
	// the two sort keys agree).
	all, err := s.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List : %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List(all) = %d ; want 2", len(all))
	}

	// Scoped List.
	scoped, _ := s.List(ctx, ListFilter{Project: "p"})
	if len(scoped) != 1 || scoped[0].UUID != "u1" {
		t.Errorf("List(p) = %v ; want [u1]", uuidsOf(scoped))
	}

	// Delete + GetMissing.
	if err := s.Delete(ctx, "u1"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, err := s.Get(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v ; want ErrNotFound", err)
	}
	// Delete-of-already-deleted reports NotFound.
	if err := s.Delete(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v ; want ErrNotFound", err)
	}
}

func uuidsOf(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.UUID
	}
	return out
}
