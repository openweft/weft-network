package router

import (
	"context"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const etcdPrefix = "/weft/network/routers/"

type etcdStore struct {
	client *clientv3.Client
}

// NewEtcd builds an etcd-backed router store.
func NewEtcd(client *clientv3.Client) Store {
	return &etcdStore{client: client}
}

func routerKey(uuid string) string { return etcdPrefix + uuid }

func (s *etcdStore) List(ctx context.Context, f ListFilter) ([]Router, error) {
	resp, err := s.client.Get(ctx, etcdPrefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd list routers : %w", err)
	}
	out := make([]Router, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var r Router
		if json.Unmarshal(kv.Value, &r) != nil {
			continue
		}
		if f.Project != "" && r.Project != f.Project {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *etcdStore) Create(ctx context.Context, r Router) (Router, error) {
	all, err := s.client.Get(ctx, etcdPrefix, clientv3.WithPrefix())
	if err != nil {
		return Router{}, fmt.Errorf("etcd router uniqueness scan : %w", err)
	}
	for _, kv := range all.Kvs {
		var existing Router
		if json.Unmarshal(kv.Value, &existing) != nil {
			continue
		}
		if existing.Project == r.Project && existing.Name == r.Name {
			return Router{}, ErrAlreadyExists
		}
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return Router{}, fmt.Errorf("encode router : %w", err)
	}
	tr, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(routerKey(r.UUID)), "=", 0)).
		Then(clientv3.OpPut(routerKey(r.UUID), string(encoded))).
		Commit()
	if err != nil {
		return Router{}, fmt.Errorf("etcd put router : %w", err)
	}
	if !tr.Succeeded {
		return Router{}, fmt.Errorf("router uuid %q already exists in etcd", r.UUID)
	}
	return r, nil
}

func (s *etcdStore) Delete(ctx context.Context, uuid string) error {
	resp, err := s.client.Delete(ctx, routerKey(uuid))
	if err != nil {
		return fmt.Errorf("etcd delete router : %w", err)
	}
	if resp.Deleted == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *etcdStore) Get(ctx context.Context, uuid string) (Router, error) {
	resp, err := s.client.Get(ctx, routerKey(uuid))
	if err != nil {
		return Router{}, fmt.Errorf("etcd get router : %w", err)
	}
	if len(resp.Kvs) == 0 {
		return Router{}, ErrNotFound
	}
	var r Router
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return Router{}, fmt.Errorf("decode router %s : %w", uuid, err)
	}
	return r, nil
}
