// The fixed v1 method registry and handler resolution (SOW-0028).
//
// Handlers receive validated params and return either the complete
// mechanically-converted result or a HandlerError. The registry is
// the single authority for method names here; unknown methods produce
// -32601 by the session. Handler families register through Register
// (their packages import rpc; rpc never imports them), and the
// advertised method list drives `system.describe` so the external
// runner skips families that are not shipped yet.

package rpc

import (
	"encoding/json"
	"sort"
	"strings"
)

// HandlerError is a product-domain failure with the documented stable
// error data: a closed adapter code list, a closed outcome list, and
// optional free-form details.
type HandlerError struct {
	Code    string
	Outcome string
	Message string
	Details any
}

func NewHandlerError(code, outcome, message string) *HandlerError {
	return &HandlerError{Code: code, Outcome: outcome, Message: message}
}

// InvalidParamsError is the conventional argument failure.
func InvalidParamsError(message string) *HandlerError {
	return NewHandlerError("invalid_argument", "not_started", message)
}

// Handler converts validated params into the complete result object.
type Handler func(st *SessionState, params json.RawMessage) (any, *HandlerError)

// ParamsValidator enforces the strict per-method params schema; a
// non-nil error becomes -32602.
type ParamsValidator func(params json.RawMessage) error

type registeredMethod struct {
	validate ParamsValidator
	handle   Handler
}

// Methods is the complete v1 method inventory in bytewise order: the
// wire authority for names. `system.describe` advertises exactly the
// inventory entries that are registered in this build (cancel is
// excluded).
var Methods = []string{
	"iprange.v1.algebra.compare",
	"iprange.v1.algebra.count",
	"iprange.v1.algebra.publish",
	"iprange.v1.cancel",
	"iprange.v1.commit.resolve",
	"iprange.v1.current.publish",
	"iprange.v1.database.create",
	"iprange.v1.database.create.resolve",
	"iprange.v1.database.info",
	"iprange.v1.database.initialize_live",
	"iprange.v1.database.live_residue.resolve",
	"iprange.v1.database.live_transition.resolve",
	"iprange.v1.database.metadata.get",
	"iprange.v1.database.metadata.replace",
	"iprange.v1.database.reclaim",
	"iprange.v1.database.reset_live",
	"iprange.v1.direct.replace",
	"iprange.v1.export",
	"iprange.v1.feeds.create",
	"iprange.v1.feeds.delete",
	"iprange.v1.feeds.import",
	"iprange.v1.feeds.rename",
	"iprange.v1.feeds.replace",
	"iprange.v1.history.project",
	"iprange.v1.join.direct",
	"iprange.v1.join.membership",
	"iprange.v1.maintenance.list",
	"iprange.v1.maintenance.remove",
	"iprange.v1.publication.inspect",
	"iprange.v1.publication.residue.remove",
	"iprange.v1.publication.resolve",
	"iprange.v1.query.cardinalities",
	"iprange.v1.query.matching_feeds",
	"iprange.v1.query.overlaps",
	"iprange.v1.reader.close",
	"iprange.v1.reader.feeds.close",
	"iprange.v1.reader.feeds.next",
	"iprange.v1.reader.feeds.open",
	"iprange.v1.reader.info",
	"iprange.v1.reader.lookup",
	"iprange.v1.reader.matching_feeds",
	"iprange.v1.reader.metadata",
	"iprange.v1.reader.open",
	"iprange.v1.reader.ranges.close",
	"iprange.v1.reader.ranges.next",
	"iprange.v1.reader.ranges.open",
	"iprange.v1.recover",
	"iprange.v1.recovery.inspect",
	"iprange.v1.retention.first_seen.refresh",
	"iprange.v1.retention.last_seen.refresh",
	"iprange.v1.snapshot",
	"iprange.v1.system.describe",
	"iprange.v1.validate",
}

var registry = make(map[string]registeredMethod)

// Register installs one callable method. Registration only accepts
// names from the fixed Methods inventory; duplicate registration of
// the same method is a programming error and panics. The cancel
// notification is handled by the transport, never registered.
func Register(method string, validate ParamsValidator, handle Handler) {
	if method == CancelMethod {
		panic("rpc: cancel notification cannot be registered")
	}
	if !methodInInventory(method) {
		panic("rpc: register of unknown method " + method)
	}
	if _, exists := registry[method]; exists {
		panic("rpc: duplicate registration of " + method)
	}
	registry[method] = registeredMethod{validate: validate, handle: handle}
}

func methodInInventory(method string) bool {
	i := sort.SearchStrings(Methods, method)
	return i < len(Methods) && Methods[i] == method
}

// Advertised returns the callable methods in bytewise order for
// system.describe: exactly the inventory entries registered in this
// build, excluding the cancel notification. It must never list a
// method that would reply -32601.
func Advertised() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		if name != CancelMethod {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// resolve returns the callable handler for one method. Method names
// reach here already validated by schema decoding (prefix checked);
// an unregistered inventory name answers -32601 by the session.
func resolve(method string) (ParamsValidator, Handler, bool) {
	entry, ok := registry[method]
	if !ok {
		return nil, nil, false
	}
	// The inventory is the authority; a registered method that is not
	// in the inventory cannot exist (Register enforces it), but the
	// guard keeps the invariant explicit for future refactors.
	if !strings.HasPrefix(method, MethodPrefix) {
		return nil, nil, false
	}
	return entry.validate, entry.handle, true
}
