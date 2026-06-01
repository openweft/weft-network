// Package statusreceiver consumes router status from the per-tenant
// NATS subject weft-router micro-VMs publish on, and feeds the
// router store's UpdateStatus method.
//
// Reverse direction of the publisher package : where publisher pushes
// DesiredState on weft.router.<uuid>.config, the receiver listens on
// weft.router.<uuid>.status (wildcard at our end : weft.router.*.status)
// and updates the corresponding Router resource's Status / PeerState
// live-state fields.
//
// Best-effort all the way through : a malformed payload logs and
// drops, an unknown uuid (Router was just deleted) is swallowed, NATS
// reconnects are handled by the nats.go client. The store keeps the
// last-good Status across restarts of weft-network ; a fresh status
// update overwrites it.
package statusreceiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openweft/weft-network/internal/store/router"
)

// Subject is the wildcard subscription pattern. weft-router publishes
// on "weft.router.<uuid>.status" ; we listen across all uuids.
const Subject = "weft.router.*.status"

// PeerStatus is one BGP neighbor's live state, mirrored from
// weft-router/internal/bgp (kept in JSON lockstep, not Go-imported).
type PeerStatus struct {
	Address          string `json:"Address"`
	State            string `json:"State"`            // "Established" | "OpenSent" | "Idle" | …
	UptimeSec        int64  `json:"UptimeSec"`        // 0 when State != Established
	ReceivedPrefixes int    `json:"ReceivedPrefixes"` // count from this neighbor
}

// RouterStatus is the JSON payload weft-router publishes periodically
// and on peer-state transitions. Maps to the Router store's Status +
// PeerState fields via deriveStatus / formatPeerState.
type RouterStatus struct {
	// Overall : "active" if every declared peer reached Established,
	// "configuring" if any is still negotiating, "down" if every peer
	// is Idle/Active. weft-router computes this server-side but the
	// receiver re-derives it from Peers so a malformed Overall doesn't
	// pin the store to a misleading state.
	Overall          string       `json:"Overall"`
	Peers            []PeerStatus `json:"Peers"`
	RoutesInstalled  int          `json:"RoutesInstalled"`
	PublishedAtUnix  int64        `json:"PublishedAtUnix"`
}

// Receiver wraps a NATS subscription and pushes incoming status
// messages to a router.Store.
type Receiver struct {
	log    *slog.Logger
	url    string
	opts   []nats.Option
	store  router.Store

	conn *nats.Conn
	sub  *nats.Subscription
}

// New constructs a Receiver. The NATS connection is dialed in Start ;
// pre-Start failures (bad URL, store nil) surface here.
func New(log *slog.Logger, url string, store router.Store, opts ...nats.Option) (*Receiver, error) {
	if log == nil {
		log = slog.Default()
	}
	if url == "" {
		return nil, fmt.Errorf("statusreceiver: empty url")
	}
	if store == nil {
		return nil, fmt.Errorf("statusreceiver: nil store")
	}
	return &Receiver{log: log, url: url, store: store, opts: opts}, nil
}

// Start dials NATS and subscribes. Returns once the subscription is
// active ; the message handler runs on the nats.Conn's goroutine pool.
// Caller calls Stop to drain.
func (r *Receiver) Start(ctx context.Context) error {
	full := append([]nats.Option{
		nats.Name("weft-network/status-receiver"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			r.log.Warn("nats disconnected (status receiver)", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			r.log.Info("nats reconnected (status receiver)", "url", c.ConnectedUrl())
		}),
	}, r.opts...)
	nc, err := nats.Connect(r.url, full...)
	if err != nil {
		return fmt.Errorf("nats connect %s: %w", r.url, err)
	}
	r.conn = nc

	sub, err := nc.Subscribe(Subject, func(m *nats.Msg) {
		r.handle(ctx, m)
	})
	if err != nil {
		nc.Drain()
		r.conn = nil
		return fmt.Errorf("subscribe %s: %w", Subject, err)
	}
	r.sub = sub
	r.log.Info("status receiver listening", "subject", Subject, "nats_url", r.url)
	return nil
}

// Stop unsubscribes and drains. Idempotent.
func (r *Receiver) Stop() {
	if r.sub != nil {
		_ = r.sub.Unsubscribe()
		r.sub = nil
	}
	if r.conn != nil {
		_ = r.conn.Drain()
		r.conn = nil
	}
}

// handle decodes one message and dispatches to the store. Errors are
// logged and dropped — weft-router will republish on its next tick,
// so a missed message just delays the live-state update by a few
// seconds rather than getting it wrong forever.
func (r *Receiver) handle(ctx context.Context, m *nats.Msg) {
	uuid, ok := uuidFromSubject(m.Subject)
	if !ok {
		r.log.Warn("status drop : subject does not match pattern", "subject", m.Subject)
		return
	}
	var st RouterStatus
	if err := json.Unmarshal(m.Data, &st); err != nil {
		r.log.Warn("status drop : malformed payload", "uuid", uuid, "err", err)
		return
	}
	status, peerState := derivedStateOf(st)
	if err := r.store.UpdateStatus(ctx, uuid, status, peerState); err != nil {
		if errors.Is(err, router.ErrNotFound) {
			// Router was deleted while a status was in flight. Expected.
			return
		}
		r.log.Warn("status drop : store update failed", "uuid", uuid, "err", err)
		return
	}
	r.log.Debug("router status updated",
		"uuid", uuid, "status", status, "peer_state", peerState,
		"routes", st.RoutesInstalled)
}
