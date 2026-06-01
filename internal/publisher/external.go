package publisher

import (
	"net"
	"strconv"
	"strings"
)

// parseExternalPeer interprets the Router.External string. Two
// shapes accepted today :
//
//   - "<peer-ip>"             : bare IP, RemoteASN left 0 (caller's
//     problem — weft-router will fail StartBgp without a remote ASN,
//     surfacing the config gap rather than silently hiding it).
//   - "<ASN>:<peer-ip>"       : the common form, e.g. "65512:198.51.100.1".
//
// Returns ok=false when the input is empty or the colon-form has
// a non-numeric ASN. Pure / no allocation in the hot path.
func parseExternalPeer(external string) (PeerConfig, bool) {
	external = strings.TrimSpace(external)
	if external == "" {
		return PeerConfig{}, false
	}
	if i := strings.LastIndexByte(external, ':'); i >= 0 {
		// IPv6 contains ':' too — distinguish by trying ParseIP on the
		// whole string first. If the whole string is a valid IP, it's
		// the bare form. Otherwise split on the LAST colon (ASN comes
		// before the address, so peer-ip is the right-hand side).
		if ip := net.ParseIP(external); ip != nil {
			return PeerConfig{Address: external}, true
		}
		asnStr := external[:i]
		peer := external[i+1:]
		asn, err := strconv.ParseUint(asnStr, 10, 32)
		if err != nil {
			return PeerConfig{}, false
		}
		if net.ParseIP(peer) == nil {
			return PeerConfig{}, false
		}
		return PeerConfig{Address: peer, RemoteASN: uint32(asn)}, true
	}
	// Bare IP, no colon.
	if net.ParseIP(external) == nil {
		return PeerConfig{}, false
	}
	return PeerConfig{Address: external}, true
}
