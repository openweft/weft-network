// Package scheduling persists Scheduling Rule resources.
//
// Today the store is the WHOLE backend for scheduling rules — no
// reconciler, no controller. The agent's FirstFitScheduler reads
// rules from this store at startup and on watch events, then enforces
// them at placement time. So implementing this domain is just :
//
//   1. CRUD against the store.
//   2. Watch events for the agent to subscribe to (future ; today
//      the agent re-reads on each scheduling pass).
package scheduling

import (
	"context"
	"errors"

	netv1 "github.com/openweft/weft-network-proto"
)

// Rule is the persisted shape of a scheduling rule. Mirrors
// netv1.SchedulingRuleInfo but holds the canonical Go time
// representation (UnixNano stamped at create time) rather than the
// proto's int64.
//
// Status + Ready are computed fields fed back by the agent's
// scheduler ; the store doesn't synthesize them. A freshly-created
// rule starts at Status="unschedulable" / Ready=0 until the agent
// reports otherwise.
type Rule struct {
	UUID         string
	Name         string
	Count        int32
	Ready        int32
	Selector     string
	AZ           string
	Rack         string
	Host         string
	Project      string
	Status       string
	CreatedAtNs  int64
}

// ToProto returns the wire representation.
func (r Rule) ToProto() *netv1.SchedulingRuleInfo {
	return &netv1.SchedulingRuleInfo{
		Uuid:           r.UUID,
		Name:           r.Name,
		Count:          r.Count,
		Ready:          r.Ready,
		Selector:       r.Selector,
		Az:             r.AZ,
		Rack:           r.Rack,
		Host:           r.Host,
		Project:        r.Project,
		Status:         r.Status,
		CreatedAtUnixNs: r.CreatedAtNs,
	}
}

// ListFilter scopes a List call. Empty Project = all projects (admin
// scope) ; non-empty Project = just that project's rules.
type ListFilter struct {
	Project string
}

// Store is the contract for scheduling-rule persistence.
//
// Method-level error contract :
//   - Create returns ErrAlreadyExists when (project, name) collide.
//     The agent + UI assume rule names are unique within a project.
//   - Delete returns ErrNotFound when UUID misses. Idempotent
//     deletes are the caller's job (the webui swallows 404 by design).
//   - Get returns ErrNotFound on miss. List never returns
//     ErrNotFound — empty filters yield empty result, not an error.
type Store interface {
	List(ctx context.Context, f ListFilter) ([]Rule, error)
	Create(ctx context.Context, r Rule) (Rule, error)
	Delete(ctx context.Context, uuid string) error
	Get(ctx context.Context, uuid string) (Rule, error)
}

// Sentinel errors ; package callers compare via errors.Is.
var (
	ErrAlreadyExists = errors.New("scheduling rule already exists")
	ErrNotFound      = errors.New("scheduling rule not found")
)
