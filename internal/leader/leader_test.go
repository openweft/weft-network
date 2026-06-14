package leader

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openweft/weft-network/internal/store/etcdtest"
)

func TestElection_AcquiresAndCallsOnAcquire(t *testing.T) {
	cli, stop := etcdtest.New(t)
	_ = stop

	e, err := New(cli, Options{Key: "/test/leader", Identity: "node-a", TTL: 5}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var acquired atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx, func(context.Context) { acquired.Add(1) }, nil)

	if !waitFor(t, 5*time.Second, func() bool { return acquired.Load() == 1 }) {
		t.Fatal("onAcquire never fired")
	}
}

func TestElection_TwoCandidatesOnlyOneLeads(t *testing.T) {
	cli, stop := etcdtest.New(t)
	_ = stop

	const key = "/test/leader-pair"
	mkE := func(id string) *Election {
		e, err := New(cli, Options{Key: key, Identity: id, TTL: 5}, nil)
		if err != nil {
			t.Fatalf("New(%s): %v", id, err)
		}
		return e
	}

	var aHas, bHas atomic.Bool
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go mkE("a").Run(ctxA, func(context.Context) { aHas.Store(true) }, func() { aHas.Store(false) })

	if !waitFor(t, 5*time.Second, func() bool { return aHas.Load() }) {
		t.Fatal("a never became leader")
	}

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go mkE("b").Run(ctxB, func(context.Context) { bHas.Store(true) }, func() { bHas.Store(false) })

	// b should NOT become leader while a holds the lease.
	time.Sleep(300 * time.Millisecond)
	if bHas.Load() {
		t.Fatal("b acquired leadership while a was holding it")
	}

	// a steps down → b takes over within one TTL window.
	cancelA()
	if !waitFor(t, 10*time.Second, func() bool { return bHas.Load() }) {
		t.Fatal("b never acquired after a stepped down")
	}
}

func TestElection_OnLostFiresOnCancel(t *testing.T) {
	cli, stop := etcdtest.New(t)
	_ = stop

	e, err := New(cli, Options{Key: "/test/leader-cancel", Identity: "node-c", TTL: 5}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var lost atomic.Int32
	acquired := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx,
		func(context.Context) {
			select {
			case acquired <- struct{}{}:
			default:
			}
		},
		func() { lost.Add(1) },
	)

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("never acquired")
	}

	cancel()

	if !waitFor(t, 5*time.Second, func() bool { return lost.Load() >= 1 }) {
		t.Fatal("onLost never fired after cancel")
	}
}

func TestNew_RejectsBadOptions(t *testing.T) {
	cli, stop := etcdtest.New(t)
	_ = stop

	cases := []struct {
		name string
		opts Options
	}{
		{"empty key", Options{Identity: "x", TTL: 5}},
		{"empty identity", Options{Key: "/k", TTL: 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(cli, c.opts, nil); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
