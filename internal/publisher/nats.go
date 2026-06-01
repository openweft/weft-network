package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/openweft/weft-network/internal/store/router"
)

// tracer is grabbed at package level — installed by main's tracing.Init
// before any handler runs, so by the time Publish/Withdraw are called
// the global TracerProvider is the OTLP-exporting one (or a no-op
// when --otlp-endpoint is empty).
var tracer trace.Tracer = otel.Tracer("github.com/openweft/weft-network/internal/publisher")

// NATS is the production RouterPublisher : connects to a NATS cluster
// and publishes / clears the per-router subject on Publish / Withdraw.
//
// We use the bare nats.Conn (no JetStream) for simplicity ; the
// subscriber side just reconciles on every message, so we don't need
// retained-message semantics. If a weft-router micro-VM boots before
// any DesiredState was published, it idles on an empty subject until
// the next CreateRouter triggers a publish — acceptable for the
// scaffold ; the production path will add a small re-publish loop or
// switch to JetStream's "last value" retention.
type NATS struct {
	conn *nats.Conn
	log  *slog.Logger
}

// NewNATS dials the given NATS URL with the provided options and
// returns a publisher. Caller owns Close ; pass the connection to a
// long-lived weft-network server.
func NewNATS(log *slog.Logger, url string, opts ...nats.Option) (*NATS, error) {
	if url == "" {
		return nil, fmt.Errorf("nats publisher: empty url")
	}
	// Forever-reconnect : if NATS hiccups, the publisher absorbs the
	// failure on the next Publish (returns an error to the caller, who
	// surfaces it as a 500 — operator retries Create).
	full := append([]nats.Option{
		nats.Name("weft-network/router-publisher"),
		nats.MaxReconnects(-1),
	}, opts...)
	nc, err := nats.Connect(url, full...)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", url, err)
	}
	return &NATS{conn: nc, log: log}, nil
}

// Close drains the NATS connection. Idempotent.
func (n *NATS) Close() {
	if n != nil && n.conn != nil {
		_ = n.conn.Drain()
		n.conn = nil
	}
}

// Publish encodes the DesiredState and publishes on SubjectFor(r.UUID).
//
// We accept empty DesiredState (kind=peer or non-gobgp egress) by
// publishing nothing — there's no weft-router subscriber listening on
// those subjects anyway. Saves wasted bytes on NATS and avoids
// poisoning a future weft-router that happens to share the uuid.
func (n *NATS) Publish(ctx context.Context, r router.Router) error {
	ctx, span := tracer.Start(ctx, "publisher.Publish",
		trace.WithAttributes(
			attribute.String("router.uuid", r.UUID),
			attribute.String("router.kind", r.Kind),
			attribute.String("router.backend", r.Backend),
		))
	defer span.End()

	state := StateFor(r)
	if len(state.Peers) == 0 && len(state.Prefixes) == 0 {
		// Nothing actionable — peer router or escape-hatch egress.
		// Log so the operator can confirm intent.
		span.SetAttributes(attribute.Bool("publish.skipped", true))
		if n.log != nil {
			n.log.Debug("router publish skipped (no actionable state)",
				"uuid", r.UUID, "kind", r.Kind, "backend", r.Backend)
		}
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal")
		return fmt.Errorf("marshal desired-state: %w", err)
	}
	subject := SubjectFor(r.UUID)
	span.SetAttributes(
		attribute.String("nats.subject", subject),
		attribute.Int("desired_state.peers", len(state.Peers)),
		attribute.Int("desired_state.prefixes", len(state.Prefixes)),
	)
	if err := n.conn.Publish(subject, payload); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "nats publish")
		return fmt.Errorf("nats publish %s: %w", subject, err)
	}
	if err := n.conn.FlushWithContext(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "nats flush")
		return fmt.Errorf("nats flush: %w", err)
	}
	if n.log != nil {
		n.log.Info("router state published",
			"uuid", r.UUID, "subject", subject, "peers", len(state.Peers))
	}
	return nil
}

// Withdraw publishes an empty payload on the subject so a freshly-booted
// weft-router subscribing with the same uuid sees "no state" rather than
// re-applying the deleted router's peers.
//
// The subscriber-side Unmarshal yields an empty DesiredState whose Apply
// calls become diff-from-current-to-empty : peers removed, prefixes
// withdrawn. Clean reconcile, no special-case.
func (n *NATS) Withdraw(ctx context.Context, uuid string) error {
	subject := SubjectFor(uuid)
	ctx, span := tracer.Start(ctx, "publisher.Withdraw",
		trace.WithAttributes(
			attribute.String("router.uuid", uuid),
			attribute.String("nats.subject", subject),
		))
	defer span.End()

	if err := n.conn.Publish(subject, []byte(`{"peers":[],"prefixes":[]}`)); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "nats publish")
		return fmt.Errorf("nats publish empty %s: %w", subject, err)
	}
	if err := n.conn.FlushWithContext(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "nats flush")
		return fmt.Errorf("nats flush: %w", err)
	}
	if n.log != nil {
		n.log.Info("router state withdrawn", "uuid", uuid, "subject", subject)
	}
	return nil
}
