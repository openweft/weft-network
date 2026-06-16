package publisher

import (
	"sync"
	"testing"
)

// TestNATS_SetFIPLookup_RaceFree exercises the concurrent path where
// the leader-election goroutine calls SetFIPLookup while gRPC
// handlers read n.fips through Publish. Pre-fix this test would
// trip Go's race detector ; post-fix the atomic.Value swap makes
// the read/write race-free. Run with `go test -race ./...`.
func TestNATS_SetFIPLookup_RaceFree(t *testing.T) {
	n := &NATS{}
	n.fips.Store(FIPLookup(NoopFIPLookup{}))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	const writers, readers = 4, 8

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					n.SetFIPLookup(NoopFIPLookup{})
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = n.fipLookup()
				}
			}
		}()
	}

	// Let them race for a moment then signal stop.
	for i := 0; i < 10000; i++ {
		_ = n.fipLookup()
	}
	close(stop)
	wg.Wait()
}
