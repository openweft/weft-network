// Package leader provides a small etcd-lease-based election so the
// weft-network daemon can run active-standby instead of a single
// non-redundant process : every instance gRPC-serves CRUD (writes
// land in the shared etcd store anyway), but only the elected
// leader runs the reactive long-loops that would otherwise duplicate
// work or race on shared state — fips.Subscriber + fips.Poller +
// statusreceiver.
//
// On leader loss, the gated loops stop cleanly ; on re-acquire (e.g.
// after a network partition recovers), they restart. Followers stay
// idle but ready to take over within one lease TTL after the leader
// dies.
//
// Implementation : go.etcd.io/etcd/client/v3/concurrency.Election.
// The lease TTL is configurable — typical production value is 10 s
// (sub-minute failover, tolerates a brief etcd hiccup).
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Election wraps an etcd lease + Campaign so callers can plug in
// onAcquire / onLost hooks without dragging concurrency.* into the
// rest of the codebase.
//
// Lifecycle :
//
//   e, err := leader.New(cli, leader.Options{Key: "/weft-network/leader", Identity: hostID, TTL: 10})
//   ctx, cancel := context.WithCancel(context.Background())
//   defer cancel()
//   if err := e.Run(ctx, onAcquire, onLost); err != nil { ... }
//
// Run blocks until ctx is cancelled. It campaigns continuously :
// if a leader exists, it waits ; once acquired, it calls onAcquire
// and watches the session ; on session loss (etcd hiccup, lease
// expiry), it calls onLost and re-campaigns.
//
// Both onAcquire and onLost run synchronously from Run's goroutine —
// callers should keep them quick (start/stop goroutines, no
// long-running work in-band). They MUST NOT block forever.
type Election struct {
	cli     *clientv3.Client
	opts    Options
	log     *slog.Logger
}

// Options carries the static config for one Election.
type Options struct {
	// Key is the etcd prefix the campaign uses. Conventionally
	// "/weft-network/leader" — sibling tools should use their own.
	Key string
	// Identity is the value stored at the leader key while held —
	// the host UUID is conventional so operator-side
	// `etcdctl get <key>` immediately shows which instance is in
	// charge.
	Identity string
	// TTL is the lease TTL in seconds. Reasonable range : 5-30 s.
	// Shorter = faster failover but more etcd chatter ; longer =
	// less chatter but longer downtime on a hard crash.
	TTL int
}

// New returns an Election. cli must outlive the Election's Run.
// nil logger defaults to slog.Default.
func New(cli *clientv3.Client, opts Options, log *slog.Logger) (*Election, error) {
	if cli == nil {
		return nil, fmt.Errorf("leader.New: nil etcd client")
	}
	if opts.Key == "" {
		return nil, fmt.Errorf("leader.New: empty Key")
	}
	if opts.Identity == "" {
		return nil, fmt.Errorf("leader.New: empty Identity")
	}
	if opts.TTL <= 0 {
		opts.TTL = 10
	}
	if log == nil {
		log = slog.Default()
	}
	return &Election{cli: cli, opts: opts, log: log}, nil
}

// Run blocks until ctx is cancelled. Campaigns continuously,
// calling onAcquire after every successful Campaign and onLost
// after every session loss. Returns ctx.Err() on cancel ; only
// returns a non-context error when the etcd client is in a state
// the loop can't recover from (e.g. closed).
func (e *Election) Run(ctx context.Context, onAcquire func(context.Context), onLost func()) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.runOnce(ctx, onAcquire, onLost); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// Transient (session lost mid-campaign, etcd
			// reconnect, ...). Back off briefly + retry. The
			// callbacks have already been notified on lost.
			e.log.Warn("leader campaign error, retrying", "key", e.opts.Key, "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

// runOnce campaigns once : create session → Campaign → block on
// session.Done() → fire onLost → return. Pulled out so the retry
// loop in Run stays declarative.
func (e *Election) runOnce(ctx context.Context, onAcquire func(context.Context), onLost func()) error {
	sess, err := concurrency.NewSession(e.cli, concurrency.WithTTL(e.opts.TTL))
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	el := concurrency.NewElection(sess, e.opts.Key)
	e.log.Info("leader campaign starting", "key", e.opts.Key, "identity", e.opts.Identity, "ttl", e.opts.TTL)
	if err := el.Campaign(ctx, e.opts.Identity); err != nil {
		return fmt.Errorf("campaign: %w", err)
	}
	e.log.Info("leader acquired", "key", e.opts.Key, "identity", e.opts.Identity)

	// Build a leader-scoped context the callback can use to gate
	// its own goroutines ; cancelled when the session goes away
	// OR when the outer ctx does, whichever fires first.
	leaderCtx, leaderCancel := context.WithCancel(ctx)
	defer leaderCancel()

	go func() {
		select {
		case <-sess.Done():
			leaderCancel()
		case <-ctx.Done():
			leaderCancel()
		}
	}()

	if onAcquire != nil {
		onAcquire(leaderCtx)
	}

	// Wait until we lose the lease or the caller cancels.
	select {
	case <-sess.Done():
		e.log.Warn("leader session lost", "key", e.opts.Key)
		if onLost != nil {
			onLost()
		}
		// Best-effort resign so a fast restart doesn't wait for
		// the lease TTL to expire on the etcd side.
		resignCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = el.Resign(resignCtx)
		cancel()
		return nil
	case <-ctx.Done():
		e.log.Info("leader giving up gracefully", "key", e.opts.Key)
		if onLost != nil {
			onLost()
		}
		resignCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = el.Resign(resignCtx)
		cancel()
		return ctx.Err()
	}
}
