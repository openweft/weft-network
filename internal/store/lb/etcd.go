package lb

import (
	"context"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const etcdPrefix = "/weft/network/loadbalancers/"

type etcdStore struct {
	client *clientv3.Client
}

// NewEtcd builds an etcd-backed LB store.
func NewEtcd(client *clientv3.Client) Store {
	return &etcdStore{client: client}
}

func lbKey(uuid string) string { return etcdPrefix + uuid }

func (s *etcdStore) List(ctx context.Context, f ListFilter) ([]LoadBalancer, error) {
	resp, err := s.client.Get(ctx, etcdPrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd list lbs : %w", err)
	}
	out := make([]LoadBalancer, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var l LoadBalancer
		if json.Unmarshal(kv.Value, &l) != nil {
			continue
		}
		if f.Project != "" && l.Project != f.Project {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *etcdStore) Create(ctx context.Context, l LoadBalancer) (LoadBalancer, error) {
	all, err := s.client.Get(ctx, etcdPrefix, clientv3.WithPrefix())
	if err != nil {
		return LoadBalancer{}, fmt.Errorf("etcd lb uniqueness scan : %w", err)
	}
	for _, kv := range all.Kvs {
		var existing LoadBalancer
		if json.Unmarshal(kv.Value, &existing) != nil {
			continue
		}
		if existing.Project == l.Project && existing.Name == l.Name {
			return LoadBalancer{}, ErrAlreadyExists
		}
	}
	encoded, err := json.Marshal(l)
	if err != nil {
		return LoadBalancer{}, fmt.Errorf("encode lb : %w", err)
	}
	tr, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(lbKey(l.UUID)), "=", 0)).
		Then(clientv3.OpPut(lbKey(l.UUID), string(encoded))).
		Commit()
	if err != nil {
		return LoadBalancer{}, fmt.Errorf("etcd put lb : %w", err)
	}
	if !tr.Succeeded {
		return LoadBalancer{}, fmt.Errorf("lb uuid %q already exists in etcd", l.UUID)
	}
	return l, nil
}

func (s *etcdStore) Delete(ctx context.Context, uuid string) error {
	resp, err := s.client.Delete(ctx, lbKey(uuid))
	if err != nil {
		return fmt.Errorf("etcd delete lb : %w", err)
	}
	if resp.Deleted == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *etcdStore) Get(ctx context.Context, uuid string) (LoadBalancer, error) {
	resp, err := s.client.Get(ctx, lbKey(uuid))
	if err != nil {
		return LoadBalancer{}, fmt.Errorf("etcd get lb : %w", err)
	}
	if len(resp.Kvs) == 0 {
		return LoadBalancer{}, ErrNotFound
	}
	var l LoadBalancer
	if err := json.Unmarshal(resp.Kvs[0].Value, &l); err != nil {
		return LoadBalancer{}, fmt.Errorf("decode lb %s : %w", uuid, err)
	}
	return l, nil
}

// SetBackends uses an optimistic-concurrency-control loop : read the
// current LB, mutate locally, write back guarded by the ModRevision
// the read returned. Two concurrent SetBackends calls can't trample
// each other ; the loser retries automatically.
func (s *etcdStore) SetBackends(ctx context.Context, uuid string, backends []string) (LoadBalancer, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := s.client.Get(ctx, lbKey(uuid))
		if err != nil {
			return LoadBalancer{}, fmt.Errorf("etcd get lb for set-backends : %w", err)
		}
		if len(resp.Kvs) == 0 {
			return LoadBalancer{}, ErrNotFound
		}
		var l LoadBalancer
		if err := json.Unmarshal(resp.Kvs[0].Value, &l); err != nil {
			return LoadBalancer{}, fmt.Errorf("decode lb : %w", err)
		}
		l.Backends = append([]string(nil), backends...)
		encoded, err := json.Marshal(l)
		if err != nil {
			return LoadBalancer{}, fmt.Errorf("encode lb : %w", err)
		}
		tr, err := s.client.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(lbKey(uuid)), "=", resp.Kvs[0].ModRevision)).
			Then(clientv3.OpPut(lbKey(uuid), string(encoded))).
			Commit()
		if err != nil {
			return LoadBalancer{}, fmt.Errorf("etcd set-backends put : %w", err)
		}
		if tr.Succeeded {
			return l, nil
		}
		// Lost the race ; another writer beat us. Retry.
	}
	return LoadBalancer{}, fmt.Errorf("set-backends conflict on %s after %d attempts", uuid, maxAttempts)
}
