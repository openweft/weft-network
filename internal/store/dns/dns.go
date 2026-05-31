// Package dns persists DNS Zone + DNS Record resources.
//
// Today the store is the WHOLE backend — no CoreDNS reconciler yet.
// Records land here, the future reconciler will materialise them into
// CoreDNS via RFC-2136 NS update or zone-file rendering. Implementing
// this domain is just :
//
//   1. CRUD against the store, with referential integrity (records
//      point at zone UUIDs).
//   2. Watch events for the future reconciler to subscribe to.
package dns

import (
	"context"
	"errors"

	netv1 "github.com/openweft/weft-network-proto"
)

// Zone is the persisted shape of a DNS zone.
//
// Records is a denormalised live count — the store maintains it on
// every record Create / Delete, so the dashboard's zone list shows
// the count without a second round-trip.
type Zone struct {
	UUID         string
	Name         string
	Role         string // "primary" | "secondary" | "forward"
	Records      int32
	TTLDefault   int32
	Backend      string // "coredns" today ; reserved for future swaps
	PushTarget   string
	PushState    string
	Project      string
	Status       string // "active" | "syncing" | "failed"
	CreatedAtNs  int64
}

// ToProto returns the wire representation.
func (z Zone) ToProto() *netv1.DNSZoneInfo {
	return &netv1.DNSZoneInfo{
		Uuid:            z.UUID,
		Name:            z.Name,
		Role:            z.Role,
		Records:         z.Records,
		TtlDefault:      z.TTLDefault,
		Backend:         z.Backend,
		PushTarget:      z.PushTarget,
		PushState:       z.PushState,
		Project:         z.Project,
		Status:          z.Status,
		CreatedAtUnixNs: z.CreatedAtNs,
	}
}

// Record is the persisted shape of a DNS record. ZoneUUID is the
// foreign key into Zone ; ZoneName is denormalised so the list view
// doesn't have to JOIN.
type Record struct {
	UUID     string
	ZoneUUID string
	ZoneName string
	Name     string // leaf or "@" for the apex
	Type     string // "A" | "AAAA" | "CNAME" | "SRV" | "TXT" | "NS" | "MX"
	Value    string
	TTL      int32
	Source   string // "static" | "auto" — only static records are operator-managed via the API
}

// ToProto returns the wire representation.
func (r Record) ToProto() *netv1.DNSRecordInfo {
	return &netv1.DNSRecordInfo{
		Uuid:     r.UUID,
		ZoneUuid: r.ZoneUUID,
		Zone:     r.ZoneName,
		Name:     r.Name,
		Type:     r.Type,
		Value:    r.Value,
		Ttl:      r.TTL,
		Source:   r.Source,
	}
}

// ZoneFilter scopes a Zone List call.
type ZoneFilter struct {
	Project string // empty = all projects
}

// RecordFilter scopes a Record List call.
type RecordFilter struct {
	ZoneUUID string // empty = all zones
}

// Store is the contract for DNS zone + record persistence.
//
// Error contract :
//   - CreateZone returns ErrAlreadyExists when (project, name) collide.
//   - CreateRecord returns ErrZoneNotFound when ZoneUUID misses.
//   - DeleteZone returns ErrNotFound on miss ; cascades record deletion
//     (it's the operator's intent : nuking a zone removes its records
//     too — symmetric with the way CoreDNS would lose them on a zone
//     reload anyway).
//   - DeleteRecord returns ErrNotFound on miss.
type Store interface {
	ListZones(ctx context.Context, f ZoneFilter) ([]Zone, error)
	CreateZone(ctx context.Context, z Zone) (Zone, error)
	DeleteZone(ctx context.Context, uuid string) error
	GetZone(ctx context.Context, uuid string) (Zone, error)

	ListRecords(ctx context.Context, f RecordFilter) ([]Record, error)
	CreateRecord(ctx context.Context, r Record) (Record, error)
	DeleteRecord(ctx context.Context, uuid string) error
	GetRecord(ctx context.Context, uuid string) (Record, error)
}

// Sentinel errors.
var (
	ErrAlreadyExists = errors.New("dns resource already exists")
	ErrNotFound      = errors.New("dns resource not found")
	ErrZoneNotFound  = errors.New("dns zone not found")
)
