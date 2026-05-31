package server

import (
	"context"
	"errors"
	"strings"
	"time"

	netv1 "github.com/openweft/weft-network-proto"
	"github.com/openweft/weft-network/internal/store/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validRecordTypes pins what CoreDNS accepts. Reject early so the
// reconciler doesn't choke on a typo three minutes later.
var validRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "SRV": true,
	"TXT": true, "NS": true, "MX": true,
}

// ---- DNS Zones ---------------------------------------------------

// ListDNSZones returns every zone, optionally scoped to a project.
func (s *Server) ListDNSZones(ctx context.Context, req *netv1.ListDNSZonesRequest) (*netv1.ListDNSZonesResponse, error) {
	zones, err := s.stores.DNS.ListZones(ctx, dns.ZoneFilter{Project: req.GetProject()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list dns zones : %v", err)
	}
	out := make([]*netv1.DNSZoneInfo, 0, len(zones))
	for _, z := range zones {
		out = append(out, z.ToProto())
	}
	return &netv1.ListDNSZonesResponse{Zones: out}, nil
}

// CreateDNSZone persists a new zone. The future CoreDNS reconciler
// picks it up via watch ; today it just lands in the store.
func (s *Server) CreateDNSZone(ctx context.Context, req *netv1.CreateDNSZoneRequest) (*netv1.CreateDNSZoneResponse, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	role := req.GetRole()
	if role == "" {
		role = "primary"
	}
	switch role {
	case "primary", "secondary", "forward":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "role %q must be primary / secondary / forward", role)
	}
	ttl := req.GetTtlDefault()
	if ttl <= 0 {
		ttl = 300 // CoreDNS default ; matches the dashboard's default placeholder
	}
	z := dns.Zone{
		UUID:        newUUID(),
		Name:        name,
		Role:        role,
		TTLDefault:  ttl,
		Backend:     "coredns",
		PushTarget:  req.GetPushTarget(),
		PushState:   "",
		Project:     req.GetProject(),
		Status:      "syncing", // until the reconciler reports active
		CreatedAtNs: time.Now().UnixNano(),
	}
	saved, err := s.stores.DNS.CreateZone(ctx, z)
	if err != nil {
		if errors.Is(err, dns.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "dns zone %q already exists in project %q", name, req.GetProject())
		}
		return nil, status.Errorf(codes.Internal, "create dns zone : %v", err)
	}
	s.logger.Info("dns zone created",
		"uuid", saved.UUID, "name", saved.Name, "project", saved.Project, "role", saved.Role)
	return &netv1.CreateDNSZoneResponse{Zone: saved.ToProto()}, nil
}

// DeleteDNSZone removes a zone and cascades its records.
func (s *Server) DeleteDNSZone(ctx context.Context, req *netv1.DeleteDNSZoneRequest) (*netv1.DeleteDNSZoneResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	if err := s.stores.DNS.DeleteZone(ctx, uuid); err != nil {
		if errors.Is(err, dns.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "dns zone %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "delete dns zone : %v", err)
	}
	s.logger.Info("dns zone deleted", "uuid", uuid)
	return &netv1.DeleteDNSZoneResponse{}, nil
}

// ---- DNS Records -------------------------------------------------

// ListDNSRecords returns records, optionally scoped to a single zone.
func (s *Server) ListDNSRecords(ctx context.Context, req *netv1.ListDNSRecordsRequest) (*netv1.ListDNSRecordsResponse, error) {
	records, err := s.stores.DNS.ListRecords(ctx, dns.RecordFilter{ZoneUUID: req.GetZoneUuid()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list dns records : %v", err)
	}
	out := make([]*netv1.DNSRecordInfo, 0, len(records))
	for _, r := range records {
		out = append(out, r.ToProto())
	}
	return &netv1.ListDNSRecordsResponse{Records: out}, nil
}

// CreateDNSRecord persists a new record under an existing zone.
// Records created via this API are always source="static" — the
// "auto" source is reserved for records the agent reconciles from
// VM lifecycle events.
func (s *Server) CreateDNSRecord(ctx context.Context, req *netv1.CreateDNSRecordRequest) (*netv1.CreateDNSRecordResponse, error) {
	zoneUUID := strings.TrimSpace(req.GetZoneUuid())
	if zoneUUID == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_uuid is required")
	}
	rtype := strings.ToUpper(strings.TrimSpace(req.GetType()))
	if !validRecordTypes[rtype] {
		return nil, status.Errorf(codes.InvalidArgument, "type %q must be one of A/AAAA/CNAME/SRV/TXT/NS/MX", req.GetType())
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required (use \"@\" for the apex)")
	}
	if strings.TrimSpace(req.GetValue()) == "" {
		return nil, status.Error(codes.InvalidArgument, "value is required")
	}
	ttl := req.GetTtl()
	if ttl <= 0 {
		// Inherit the zone's default TTL when the request leaves it unset.
		z, err := s.stores.DNS.GetZone(ctx, zoneUUID)
		if err == nil {
			ttl = z.TTLDefault
		}
		if ttl <= 0 {
			ttl = 300
		}
	}
	r := dns.Record{
		UUID:     newUUID(),
		ZoneUUID: zoneUUID,
		Name:     name,
		Type:     rtype,
		Value:    req.GetValue(),
		TTL:      ttl,
		Source:   "static",
	}
	saved, err := s.stores.DNS.CreateRecord(ctx, r)
	if err != nil {
		if errors.Is(err, dns.ErrZoneNotFound) {
			return nil, status.Errorf(codes.NotFound, "dns zone %q not found", zoneUUID)
		}
		return nil, status.Errorf(codes.Internal, "create dns record : %v", err)
	}
	s.logger.Info("dns record created",
		"uuid", saved.UUID, "zone", saved.ZoneName, "name", saved.Name, "type", saved.Type)
	return &netv1.CreateDNSRecordResponse{Record: saved.ToProto()}, nil
}

// DeleteDNSRecord removes a record by uuid.
func (s *Server) DeleteDNSRecord(ctx context.Context, req *netv1.DeleteDNSRecordRequest) (*netv1.DeleteDNSRecordResponse, error) {
	uuid := strings.TrimSpace(req.GetUuid())
	if uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "uuid is required")
	}
	if err := s.stores.DNS.DeleteRecord(ctx, uuid); err != nil {
		if errors.Is(err, dns.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "dns record %q not found", uuid)
		}
		return nil, status.Errorf(codes.Internal, "delete dns record : %v", err)
	}
	s.logger.Info("dns record deleted", "uuid", uuid)
	return &netv1.DeleteDNSRecordResponse{}, nil
}
