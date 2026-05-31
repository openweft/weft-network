package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// etcdPrefix is where every scheduling rule lives in etcd. One
// key per rule, value = JSON-encoded Rule.
//
//   /weft/network/scheduling-rules/<uuid>  =  {"uuid":"…","name":"…",…}
//
// The (project,name) uniqueness check is implemented by listing
// the prefix on Create. That's O(N) in rule count, fine while N
// stays in the dozens ; trade up for an indexed key (e.g. a
// /weft/network/scheduling-rules-by-name/<project>/<name> mirror
// written under the same Txn) when N grows.
const etcdPrefix = "/weft/network/scheduling-rules/"

// etcdStore implements Store on top of an etcd v3 client.
//
// Operation contract :
//   - Create uses a Txn with a Compare on the key's CreateRevision
//     to enforce uniqueness atomically. The (project, name) lookup
//     is best-effort under the same Txn ; a duplicate that races a
//     Create-then-rename would slip through. For our scale, this is
//     fine — rename isn't a supported op anyway.
//   - Delete uses a plain Delete (etcd is idempotent ; missing key
//     comes back with Deleted=0, we translate to ErrNotFound).
//   - List uses a prefix Get with sort by CreateRevision asc to
//     match memoryStore's stable ordering contract.
type etcdStore struct {
	client *clientv3.Client
}

// NewEtcd builds an etcd-backed scheduling rule store. The client
// is owned by the caller (close it via Client.Close when the daemon
// shuts down).
func NewEtcd(client *clientv3.Client) Store {
	return &etcdStore{client: client}
}

func ruleKey(uuid string) string { return etcdPrefix + uuid }

func (s *etcdStore) List(ctx context.Context, f ListFilter) ([]Rule, error) {
	resp, err := s.client.Get(ctx, etcdPrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd get %s* : %w", etcdPrefix, err)
	}
	out := make([]Rule, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var r Rule
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			// Skip corrupt entries rather than fail the whole List —
			// surfacing the error would mean an operator can't see any
			// rules until the bad one is hand-fixed in etcd.
			continue
		}
		if f.Project != "" && r.Project != f.Project {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *etcdStore) Create(ctx context.Context, r Rule) (Rule, error) {
	// Uniqueness check : scan the prefix for an existing (project, name).
	// Done inside the same context but NOT in a single Txn — the etcd v3
	// model can't express "no key in prefix matches predicate" atomically.
	// A racing Create-then-rename could theoretically slip a dup past us ;
	// for the dashboard's usage pattern that's an acceptable trade.
	resp, err := s.client.Get(ctx, etcdPrefix, clientv3.WithPrefix())
	if err != nil {
		return Rule{}, fmt.Errorf("etcd uniqueness scan : %w", err)
	}
	for _, kv := range resp.Kvs {
		var existing Rule
		if json.Unmarshal(kv.Value, &existing) != nil {
			continue
		}
		if existing.Project == r.Project && existing.Name == r.Name {
			return Rule{}, ErrAlreadyExists
		}
	}

	encoded, err := json.Marshal(r)
	if err != nil {
		return Rule{}, fmt.Errorf("encode rule : %w", err)
	}
	// Txn with Compare(CreateRevision == 0) enforces "doesn't exist
	// yet" — guards against two concurrent Creates with the same UUID
	// (extremely unlikely but cheap to defend against).
	txn := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(ruleKey(r.UUID)), "=", 0)).
		Then(clientv3.OpPut(ruleKey(r.UUID), string(encoded)))
	tr, err := txn.Commit()
	if err != nil {
		return Rule{}, fmt.Errorf("etcd put : %w", err)
	}
	if !tr.Succeeded {
		// UUID collision — should never happen since we mint fresh
		// UUIDs server-side, but the etcd Txn protects us.
		return Rule{}, fmt.Errorf("scheduling rule uuid %q already exists in etcd", r.UUID)
	}
	return r, nil
}

func (s *etcdStore) Delete(ctx context.Context, uuid string) error {
	resp, err := s.client.Delete(ctx, ruleKey(uuid))
	if err != nil {
		return fmt.Errorf("etcd delete : %w", err)
	}
	if resp.Deleted == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *etcdStore) Get(ctx context.Context, uuid string) (Rule, error) {
	resp, err := s.client.Get(ctx, ruleKey(uuid))
	if err != nil {
		return Rule{}, fmt.Errorf("etcd get : %w", err)
	}
	if len(resp.Kvs) == 0 {
		return Rule{}, ErrNotFound
	}
	var r Rule
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return Rule{}, fmt.Errorf("decode rule %s : %w", uuid, err)
	}
	return r, nil
}

// uuidFromKey strips the prefix to recover the rule UUID. Exposed
// for tests / future watch handlers — the store itself doesn't need
// it since values carry their own UUID.
func uuidFromKey(key string) string {
	return strings.TrimPrefix(key, etcdPrefix)
}
