// Core-level edit entry points for the public workflows (Rust
// LiveWriter::mutate: one DraftStore binding per operation over the open
// draft). The store is a stateless view over (mapping, committed page
// count, budget, draft); the draft carries all mutation state, so binding
// it per operation is semantically identical to the Rust edit core that
// holds one store for the draft lifetime. The workflow-level family,
// ordering, and metadata-stage gates live in the public facade exactly
// like Rust require_direct; these entry points assume a gated caller
// (Rust DraftStore parity).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// AssignV4 assigns one inclusive IPv4 interval on the open draft (Rust
// DraftStore::assign_v4).
func (c *Core) AssignV4(from, to uint32, value uint32) (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).AssignV4(from, to, value)
}

// AssignV6 assigns one inclusive IPv6 interval on the open draft (Rust
// DraftStore::assign_v6).
func (c *Core) AssignV6(fromHi, fromLo, toHi, toLo uint64, value uint32) (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).AssignV6(fromHi, fromLo, toHi, toLo, value)
}

// ClearV4 clears one inclusive IPv4 interval on the open draft (Rust
// DraftStore::clear_v4).
func (c *Core) ClearV4(from, to uint32) (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).ClearV4(from, to)
}

// ClearV6 clears one inclusive IPv6 interval on the open draft (Rust
// DraftStore::clear_v6).
func (c *Core) ClearV6(fromHi, fromLo, toHi, toLo uint64) (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).ClearV6(fromHi, fromLo, toHi, toLo)
}

// SetMetadata stages one exact metadata replacement on the open draft
// (Rust DraftStore::set_metadata).
func (c *Core) SetMetadata(input []byte) (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).SetMetadata(input)
}

// ClearMetadata stages metadata absence on the open draft (Rust
// DraftStore::clear_metadata).
func (c *Core) ClearMetadata() (bool, error) {
	if err := c.requireHealthy(); err != nil {
		return false, err
	}
	if c.draft == nil {
		return false, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft).ClearMetadata()
}
