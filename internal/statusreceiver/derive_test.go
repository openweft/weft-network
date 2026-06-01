package statusreceiver

import (
	"testing"
)

func TestRollupStatus(t *testing.T) {
	cases := []struct {
		name  string
		peers []PeerStatus
		want  string
	}{
		{"no peers", nil, "configuring"},
		{"all established", []PeerStatus{{State: "Established"}, {State: "Established"}}, "active"},
		{"one established, one negotiating", []PeerStatus{{State: "Established"}, {State: "OpenSent"}}, "active"},
		{"some negotiating, none established", []PeerStatus{{State: "OpenSent"}, {State: "Idle"}}, "configuring"},
		{"all idle", []PeerStatus{{State: "Idle"}, {State: "Idle"}}, "down"},
		{"mix idle+connect", []PeerStatus{{State: "Connect"}, {State: "Idle"}}, "down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollupStatus(tc.peers); got != tc.want {
				t.Errorf("rollupStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatPeerState(t *testing.T) {
	st := RouterStatus{
		Peers: []PeerStatus{
			{Address: "198.51.100.1", State: "Established"},
			{Address: "198.51.100.2", State: "Idle"},
		},
		RoutesInstalled: 42,
	}
	got := formatPeerState(st)
	want := "198.51.100.1:Established,198.51.100.2:Idle ; routes=42"
	if got != want {
		t.Errorf("formatPeerState = %q\nwant %q", got, want)
	}
	if got := formatPeerState(RouterStatus{}); got != "" {
		t.Errorf("empty Peers → expected empty string, got %q", got)
	}
}

func TestUUIDFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		wantOK  bool
		want    string
	}{
		{"weft.router.abc-123.status", true, "abc-123"},
		{"weft.router..status", false, ""},
		{"weft.router.a.b.status", false, ""},        // extra dot in middle
		{"other.subject", false, ""},
		{"weft.router.xxx.config", false, ""},        // wrong suffix
		{"", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.subject, func(t *testing.T) {
			got, ok := uuidFromSubject(tc.subject)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("uuidFromSubject(%q) = (%q, %v), want (%q, %v)",
					tc.subject, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestDerivedStateOf_End2End(t *testing.T) {
	// The wire-shape pin-down test : decode a representative
	// RouterStatus, run derivedStateOf, assert the (status, peerState)
	// pair matches what weft-router → weft-network observers see.
	st := RouterStatus{
		Overall:         "active",
		RoutesInstalled: 17,
		Peers: []PeerStatus{
			{Address: "203.0.113.1", State: "Established", UptimeSec: 600, ReceivedPrefixes: 5},
		},
	}
	status, peerState := derivedStateOf(st)
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
	if peerState != "203.0.113.1:Established ; routes=17" {
		t.Errorf("peerState = %q", peerState)
	}
}
