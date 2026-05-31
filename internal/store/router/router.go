// Package router persists Router resources : mesh ↔ mesh peering
// (WireGuard) or mesh ↔ outside (VyOS / FRR BGP egress + NAT).
//
// Today the store is the WHOLE backend — the data-plane reconciler
// (WireGuard peer config push, BGP daemon programming) lands in a
// follow-on commit. Implementing this domain is just CRUD with
// kind / backend validation.
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
	UUID         string
	Name         string
	Kind         string   // "peer" | "egress"
	Backend      string   // "wireguard" | "vyos" | "frr"
	Networks     []string // tenant networks this router stitches
	External     string   // AS number / peer IP — only for kind=egress
	PeerState    string
	Project      string
	Status       string
	CreatedAtNs  int64
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
}

// Sentinel errors.
var (
	ErrAlreadyExists = errors.New("router already exists")
	ErrNotFound      = errors.New("router not found")
)
