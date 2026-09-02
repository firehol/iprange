// Handler-family registration for the product executable (the lead
// owns the wiring; each family worker provides its Register*
// function). cmd/iprange calls RegisterAll before the JSON-RPC
// session starts. Registration is additive and idempotent per build:
// the rpc registry panics on duplicate or unknown names, so a family
// can only register once. Families that are not shipped yet are
// simply not registered: system.describe advertises exactly the
// registered inventory and the external runner skips unadvertised
// methods.

package handlers

import (
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// RegisterAll installs every implemented handler family, in the fixed
// step-2 delivery order (system, reader/cursors, algebra/query,
// publish/export, live/feeds, lifecycle/maintenance/snapshot/
// recovery).
func RegisterAll() {
	registerSystem()
	RegisterReader()
	RegisterCursors()
	RegisterAlgebra()
	RegisterPublish()
	RegisterExport()
	RegisterLive()
	RegisterFeeds()
	RegisterLifecycle()
	RegisterMaintenance()
	RegisterSnapshot()
	RegisterRecovery()
}

func registerSystem() {
	rpc.Register("iprange.v1.system.describe", ValidateDescribeParams, Describe)
}
