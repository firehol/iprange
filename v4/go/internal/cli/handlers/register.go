// Handler-family registration for the product executable. The lead
// integrates each family's Register* function here as its handlers
// land; iprange/cmd calls RegisterAll before Run. Registration is
// additive and idempotent per build: the rpc registry panics on
// duplicate or unknown names, so a family can only register once.

package handlers

import (
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// RegisterAll installs every implemented handler family. Families
// that are not shipped yet are simply not registered: system.describe
// advertises exactly the registered inventory, and the external
// runner skips unadvertised methods.
func RegisterAll() {
	registerSystem()
}

func registerSystem() {
	rpc.Register("iprange.v1.system.describe", ValidateDescribeParams, Describe)
}
