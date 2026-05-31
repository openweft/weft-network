// Package store defines the persistence interfaces every weft-network
// reconciler reads from / writes to. Two backends today :
//
//   - memory.New() — in-process map, restart loses everything. Dev /
//     test only ; selected when --etcd is empty.
//   - etcd.New() — production. Not yet implemented ; see [[project_weft_network]].
//
// The store split is deliberate. Each domain (scheduling rules, DNS
// zones, DNS records, routers, LBs) has its own narrow interface so
// a partial migration to etcd can flip one domain at a time, mixing
// memory and etcd backends across the daemon.
package store

import (
	"github.com/openweft/weft-network/internal/store/dns"
	"github.com/openweft/weft-network/internal/store/router"
	"github.com/openweft/weft-network/internal/store/scheduling"
)

// Stores aggregates one backend per domain. The daemon wires them
// once at startup ; handlers consume the interfaces and don't care
// about the impl.
type Stores struct {
	SchedulingRules scheduling.Store
	DNS             dns.Store
	Routers         router.Store
}
