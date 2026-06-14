package fips

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// platformEvent mirrors the weft.PlatformEvent JSON shape published
// on the NATS subject "weft.events.<kind>". Duplicated here rather
// than imported because weft-network must not depend on weft's
// module graph — same convention publisher.PeerConfig uses for the
// wire-shape with weft-router.
type platformEvent struct {
	TsUnixNano  int64             `json:"TsUnixNano"`
	Kind        string            `json:"Kind"`
	Subject     string            `json:"Subject"`
	ProjectUUID string            `json:"ProjectUUID"`
	Meta        map[string]string `json:"Meta"`
}

// Subscriber listens for floating-ip platform events on
// "weft.events.floating_ip.>" and updates the in-memory Index. Run
// blocks until ctx is cancelled.
//
// The wildcard subscription pulls all four kinds (allocated /
// released / mapped / unmapped). The handler translates each kind
// into an Index.Upsert or Index.Delete :
//
//   floating_ip.allocated → Upsert(Mapped=false)  // tracked but not surfaced
//   floating_ip.mapped    → Upsert(Mapped=true)
//   floating_ip.unmapped  → Upsert(Mapped=false)
//   floating_ip.released  → Delete
//
// On every relevant event, the subscriber also calls the optional
// OnChange callback so the caller can trigger a republish for every
// router that stitches the affected network — the wiring point in
// cmd/weft-network/main.go.
type Subscriber struct {
	idx      *Index
	conn     *nats.Conn
	log      *slog.Logger
	onChange func(networkUUID string)
}

// NewSubscriber builds a Subscriber. onChange may be nil ; the index
// still updates, but the publisher won't be re-fired (only freshly-
// created routers will pick up the latest state).
func NewSubscriber(conn *nats.Conn, idx *Index, log *slog.Logger, onChange func(networkUUID string)) (*Subscriber, error) {
	if conn == nil {
		return nil, fmt.Errorf("fips.NewSubscriber: nil nats conn")
	}
	if idx == nil {
		return nil, fmt.Errorf("fips.NewSubscriber: nil index")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Subscriber{idx: idx, conn: conn, log: log, onChange: onChange}, nil
}

// Subject is the wildcard the subscriber listens on. Exported for
// tests + so the cmd/weft-network startup log line can mention it.
const Subject = "weft.events.floating_ip.>"

// Run subscribes to the NATS wildcard and blocks until ctx is
// cancelled, draining the subscription on exit so a daemon shutdown
// doesn't leak a callback goroutine.
func (s *Subscriber) Run(ctx context.Context) error {
	sub, err := s.conn.Subscribe(Subject, func(m *nats.Msg) {
		s.handle(m.Subject, m.Data)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", Subject, err)
	}
	// Flush so the SUB protocol message reaches the server before
	// the caller can start producing events ; otherwise a publish
	// landing in the same millisecond races the subscription.
	if err := s.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return fmt.Errorf("flush after subscribe: %w", err)
	}
	defer sub.Unsubscribe()
	s.log.Info("fips subscriber listening", "subject", Subject)
	<-ctx.Done()
	return ctx.Err()
}

// HandleMessage is the testable seam : feed it a raw subject + JSON
// payload and observe the index. Forwards to the private handle.
func (s *Subscriber) HandleMessage(subject string, data []byte) {
	s.handle(subject, data)
}

func (s *Subscriber) handle(subject string, data []byte) {
	var ev platformEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.log.Warn("fips: malformed event payload", "subject", subject, "err", err)
		return
	}
	entry := Entry{
		UUID:        ev.Subject,
		ProjectUUID: ev.ProjectUUID,
		NetworkUUID: ev.Meta["network_uuid"],
		Address:     ev.Meta["address"],
		TargetKind:  ev.Meta["target_kind"],
		Target:      ev.Meta["target"],
	}
	var changedNetwork string
	ensureRegistered()
	switch ev.Kind {
	case "floating_ip.allocated":
		entry.Mapped = false
		entry.TargetKind = ""
		entry.Target = ""
		s.idx.Upsert(entry)
		changedNetwork = entry.NetworkUUID
		subscriberEventsTotal.WithLabelValues("allocated").Inc()
	case "floating_ip.mapped":
		entry.Mapped = true
		s.idx.Upsert(entry)
		changedNetwork = entry.NetworkUUID
		subscriberEventsTotal.WithLabelValues("mapped").Inc()
	case "floating_ip.unmapped":
		entry.Mapped = false
		entry.TargetKind = ""
		entry.Target = ""
		s.idx.Upsert(entry)
		changedNetwork = entry.NetworkUUID
		subscriberEventsTotal.WithLabelValues("unmapped").Inc()
	case "floating_ip.released":
		prev := s.idx.Delete(ev.Subject)
		changedNetwork = prev.NetworkUUID
		subscriberEventsTotal.WithLabelValues("released").Inc()
	default:
		// Wildcard occasionally surfaces siblings we don't react
		// to ; silently drop.
		subscriberEventsTotal.WithLabelValues("unknown").Inc()
		return
	}
	if changedNetwork != "" && s.onChange != nil {
		s.onChange(changedNetwork)
	}
}
