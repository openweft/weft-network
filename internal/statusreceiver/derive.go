package statusreceiver

import (
	"fmt"
	"strings"
)

// derivedStateOf computes the (Router.Status, Router.PeerState) pair
// from one RouterStatus message. Status is the rolled-up health,
// PeerState is the human-readable peer summary that shows in the
// dashboard's "peer state" column.
//
// We re-derive Status from Peers rather than trusting RouterStatus.
// Overall so a malformed Overall from a misbehaving emitter can't pin
// the store to a state inconsistent with its own peer list. Worst
// case: emitter and receiver disagree on Overall ; receiver wins,
// next message overwrites.
//
// Pure / no I/O — table-testable.
func derivedStateOf(st RouterStatus) (status, peerState string) {
	status = rollupStatus(st.Peers)
	peerState = formatPeerState(st)
	return status, peerState
}

// rollupStatus :
//
//	zero peers      → "configuring" (router persists, no peer reached us yet)
//	all Established → "active"
//	some Established → "active" (degraded but functional)
//	none Established + something negotiating (OpenSent/OpenConfirm) → "configuring"
//	everything Idle/Active/Connect → "down"
func rollupStatus(peers []PeerStatus) string {
	if len(peers) == 0 {
		return "configuring"
	}
	anyEstablished := false
	anyNegotiating := false
	for _, p := range peers {
		switch p.State {
		case "Established":
			anyEstablished = true
		case "OpenSent", "OpenConfirm", "Active":
			anyNegotiating = true
		}
	}
	switch {
	case anyEstablished:
		return "active"
	case anyNegotiating:
		return "configuring"
	default:
		return "down"
	}
}

// formatPeerState produces the dashboard's "peer state" column —
// a short list of "<addr>:<state>" entries, plus the route count.
// Empty when no peers.
func formatPeerState(st RouterStatus) string {
	if len(st.Peers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(st.Peers))
	for _, p := range st.Peers {
		parts = append(parts, fmt.Sprintf("%s:%s", p.Address, p.State))
	}
	return fmt.Sprintf("%s ; routes=%d", strings.Join(parts, ","), st.RoutesInstalled)
}

// uuidFromSubject extracts the router uuid from a NATS subject of
// shape "weft.router.<uuid>.status". Returns ok=false on any
// shape mismatch — handle() drops the message in that case.
func uuidFromSubject(subject string) (string, bool) {
	const prefix = "weft.router."
	const suffix = ".status"
	if !strings.HasPrefix(subject, prefix) || !strings.HasSuffix(subject, suffix) {
		return "", false
	}
	uuid := strings.TrimSuffix(strings.TrimPrefix(subject, prefix), suffix)
	if uuid == "" || strings.ContainsRune(uuid, '.') {
		// Defend against malformed subjects like "weft.router..status"
		// or "weft.router.a.b.status".
		return "", false
	}
	return uuid, true
}
