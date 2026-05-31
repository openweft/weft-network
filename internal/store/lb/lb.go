// Package lb persists Load Balancer resources.
//
// Today the store is the WHOLE backend — the data plane (Caddy
// embedded in each host's weft-agent, see
// weft/agent/proxy/) is programmed by a future reconciler that
// diffs LB.Backends against each host's currently-installed Caddy
// config and POSTs deltas via the local admin socket.
//
// The historical Envoy / xDS comment in network.proto predates the
// Caddy-in-weft-agent decision (project_reverse_proxy_caddy memory) ;
// the message shape (mode / address / port / backends / controller)
// still fits.
package lb

import (
	"context"
	"errors"

	netv1 "github.com/openweft/weft-network-proto"
)

// LoadBalancer is the persisted shape.
type LoadBalancer struct {
	UUID        string
	Name        string
	Mode        string // "L4" | "L7"
	Address     string // VIP
	Port        uint32
	Backends    []string
	AZ          string // "DC-A" / "DC-B" / "DC-C" / "multi"
	Controller  string // the weft-network replica currently owning the reconcile stream
	Project     string
	Status      string // "active" | "provisioning" | "failed"
	CreatedAtNs int64
}

// ToProto returns the wire shape.
func (l LoadBalancer) ToProto() *netv1.LoadBalancerInfo {
	return &netv1.LoadBalancerInfo{
		Uuid:            l.UUID,
		Name:            l.Name,
		Mode:            l.Mode,
		Address:         l.Address,
		Port:            l.Port,
		Backends:        append([]string(nil), l.Backends...),
		Az:              l.AZ,
		Controller:      l.Controller,
		Project:         l.Project,
		Status:          l.Status,
		CreatedAtUnixNs: l.CreatedAtNs,
	}
}

// ListFilter scopes List calls.
type ListFilter struct {
	Project string
}

// Store is the contract for LB persistence.
//
// SetBackends atomically replaces the backend list. The reconciler
// later diffs against the live Caddy config and POSTs deltas. The
// "atomic" guarantee here is in-memory ; the future etcd backend
// upgrades it to a real CAS so two concurrent SetBackends calls
// can't interleave.
type Store interface {
	List(ctx context.Context, f ListFilter) ([]LoadBalancer, error)
	Create(ctx context.Context, l LoadBalancer) (LoadBalancer, error)
	Delete(ctx context.Context, uuid string) error
	Get(ctx context.Context, uuid string) (LoadBalancer, error)
	SetBackends(ctx context.Context, uuid string, backends []string) (LoadBalancer, error)
}

// Sentinel errors.
var (
	ErrAlreadyExists = errors.New("load balancer already exists")
	ErrNotFound      = errors.New("load balancer not found")
)
