// Package router persists Router resources : mesh ↔ mesh peering
// (WireGuard) or mesh ↔ outside (GoBGP micro-VM as the default egress
// backend, VyOS / FRR classic VMs as the escape hatch).
//
// Today the store is the WHOLE backend — the data-plane reconciler
// (WireGuard peer config push, GoBGP DesiredState publish on NATS via
// internal/publisher) lands in a follow-on commit. Implementing this
// domain is just CRUD with kind / backend validation.
package router

import (
	"context"
	"errors"

	netv1 "github.com/openweft/weft-network-proto"
)

// Router is the persisted shape.
//
// Status + PeerState are computed fields the data-plane reconciler
// feeds back ; a freshly-created router starts at status="configuring"
// / peer_state="" until the reconciler reports otherwise.
type Router struct {
	UUID        string
	Name        string
	Kind        string   // "peer" | "egress"
	Backend     string   // "wireguard" | "gobgp" | "vyos" | "frr"
	Networks    []string // tenant networks this router stitches
	External    string   // AS number / peer IP — only for kind=egress
	PeerState   string
	Project     string
	Status      string
	CreatedAtNs int64
}

// ToProto returns the wire shape.
func (r Router) ToProto() *netv1.RouterInfo {
	return &netv1.RouterInfo{
		Uuid:            r.UUID,
		Name:            r.Name,
		Kind:            r.Kind,
		Backend:         r.Backend,
		Networks:        append([]string(nil), r.Networks...),
		External:        r.External,
		PeerState:       r.PeerState,
		Project:         r.Project,
		Status:          r.Status,
		CreatedAtUnixNs: r.CreatedAtNs,
	}
}

// ListFilter scopes a List call.
type ListFilter struct {
	Project string
}

// Store is the contract for router persistence.
type Store interface {
	List(ctx context.Context, f ListFilter) ([]Router, error)
	Create(ctx context.Context, r Router) (Router, error)
	Delete(ctx context.Context, uuid string) error
	Get(ctx context.Context, uuid string) (Router, error)
	// UpdateStatus mutates the live-state fields (status / peer_state)
	// without touching the operator-set desired-state. Called by the
	// status receiver when a weft-router micro-VM publishes its current
	// peer state. Returns ErrNotFound when the uuid isn't known —
	// receivers swallow that to absorb the race where a Router is
	// deleted while a status message was in flight.
	UpdateStatus(ctx context.Context, uuid, status, peerState string) error
}

// Sentinel errors.
var (
	ErrAlreadyExists = errors.New("router already exists")
	ErrNotFound      = errors.New("router not found")
)
