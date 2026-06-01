package publisher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openweft/weft-network/internal/store/router"
)

func TestSubjectFor(t *testing.T) {
	got := SubjectFor("ten-abcd")
	want := "weft.router.ten-abcd.config"
	if got != want {
		t.Errorf("SubjectFor = %q, want %q", got, want)
	}
}

func TestParseExternalPeer(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantAddr string
		wantASN  uint32
	}{
		{"", false, "", 0},
		{"198.51.100.1", true, "198.51.100.1", 0},
		{"65512:198.51.100.1", true, "198.51.100.1", 65512},
		{"2001:db8::1", true, "2001:db8::1", 0},   // IPv6 bare
		{"not-an-ip", false, "", 0},
		{"abc:198.51.100.1", false, "", 0},        // non-numeric ASN
		{"65512:not-an-ip", false, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseExternalPeer(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Address != tc.wantAddr || got.RemoteASN != tc.wantASN {
				t.Errorf("got {%s, %d}, want {%s, %d}",
					got.Address, got.RemoteASN, tc.wantAddr, tc.wantASN)
			}
		})
	}
}

func TestStateFor(t *testing.T) {
	// kind=peer → empty state (WireGuard is reconciled by a different surface).
	if s := StateFor(router.Router{Kind: "peer", Backend: "wireguard"}); len(s.Peers) != 0 || len(s.Prefixes) != 0 {
		t.Errorf("peer router shouldn't emit state: %+v", s)
	}
	// kind=egress + backend=vyos → empty (classic-VM escape hatch, not weft-router).
	if s := StateFor(router.Router{Kind: "egress", Backend: "vyos", External: "198.51.100.1"}); len(s.Peers) != 0 {
		t.Errorf("vyos egress shouldn't emit state: %+v", s)
	}
	// kind=egress + backend=gobgp → one peer parsed from External.
	r := router.Router{Kind: "egress", Backend: "gobgp", External: "65512:198.51.100.1"}
	s := StateFor(r)
	if len(s.Peers) != 1 {
		t.Fatalf("gobgp egress should emit one peer, got %d", len(s.Peers))
	}
	if s.Peers[0].Address != "198.51.100.1" || s.Peers[0].RemoteASN != 65512 {
		t.Errorf("peer parsed wrong: %+v", s.Peers[0])
	}
}

func TestStateForWireRoundTrip(t *testing.T) {
	// The JSON shape is the actual contract with weft-router/internal/
	// subscriber.DesiredState. Pin it here so a future field rename
	// trips this test before silently breaking the reconcile loop.
	r := router.Router{Kind: "egress", Backend: "gobgp", External: "65000:203.0.113.1"}
	state := StateFor(r)
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Decode into a structural mirror to assert exact wire keys.
	var mirror struct {
		Peers []struct {
			Address   string `json:"Address"`
			RemoteASN uint32 `json:"RemoteASN"`
		} `json:"peers"`
		Prefixes []any `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &mirror); err != nil {
		t.Fatalf("unmarshal mirror: %v ; raw=%s", err, b)
	}
	if len(mirror.Peers) != 1 || mirror.Peers[0].Address != "203.0.113.1" || mirror.Peers[0].RemoteASN != 65000 {
		t.Errorf("wire shape mismatch: %s", b)
	}
}

func TestNoopDoesntPanic(t *testing.T) {
	// Default for the Server when no NATS publisher is wired. Exercise
	// both methods to make sure neither dereferences a nil logger.
	n := Noop{}
	if err := n.Publish(context.Background(), router.Router{UUID: "u"}); err != nil {
		t.Errorf("Noop.Publish: %v", err)
	}
	if err := n.Withdraw(context.Background(), "u"); err != nil {
		t.Errorf("Noop.Withdraw: %v", err)
	}
}
