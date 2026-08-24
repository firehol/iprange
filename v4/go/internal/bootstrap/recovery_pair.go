package bootstrap

// Recovery meta pair classification and exact recovery-candidate
// selection (Rust recovery/classify.rs ClassifiedMetas): the two
// per-page states and the derived generation order. The recovery
// package composes this core for its candidate projection and
// progress; the live package composes it for the recovery-candidate
// live source binding, so the order proof has one authority.

import "github.com/firehol/iprange/v4/go/internal/format"

// RecoveryCandidateToken is the exact cross-package selection token of
// one recoverable retained meta page (Rust RecoveryCandidate minus the
// label and portable-identity limbs, which the recovery package keeps
// on its public token surface). The device+inode pair stands for the
// source identity; the label is enforced by the token producer.
type RecoveryCandidateToken struct {
	MetaPage      uint8
	Device        uint64
	Inode         uint64
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}

// RecoveryMetaPair is the classified two-page recovery meta pair
// (Rust ClassifiedMetas): the raw states, the presence flags, and the
// proven generation order.
type RecoveryMetaPair struct {
	states      [2]RecoveryMetaState
	has         [2]bool
	proven      bool
	current     uint8
	hasPrevious bool
	previous    uint8
}

// ClassifyRecoveryMetaPair derives the order from two raw meta states
// (Rust ClassifiedMetas::new over classify_order: both states present
// with valid order arms and equal static identity, then
// equal-transaction or adjacent-transaction proof).
func ClassifyRecoveryMetaPair(states [2]RecoveryMetaState, has [2]bool) RecoveryMetaPair {
	pair := RecoveryMetaPair{states: states, has: has}
	if !has[0] || !has[1] {
		return pair
	}
	s0, s1 := states[0], states[1]
	if !s0.OrderValid || !s1.OrderValid {
		return pair
	}
	m0, m1 := s0.Order, s1.Order
	if !staticIdentityEqual(m0, m1) {
		return pair
	}
	switch {
	case m0.TxnID == m1.TxnID:
		if !metaEqual(m0, m1) {
			return pair
		}
		pair.proven = true
		pair.current = uint8(m0.TxnID & 1)
	case m0.TxnID == m1.TxnID+1:
		pair.applyAdjacent(m1, m0, 0)
	case m1.TxnID == m0.TxnID+1:
		pair.applyAdjacent(m0, m1, 1)
	}
	return pair
}

// applyAdjacent proves the order of one adjacent pair where higher is
// the newer meta on higherPage (Rust adjacent_order over
// checked_add: the lower transaction must advance without wrapping,
// and the parity of the higher page must match its transaction).
func (p *RecoveryMetaPair) applyAdjacent(lower, higher format.Meta, higherPage uint8) {
	if lower.TxnID == ^uint64(0) || lower.TxnID+1 != higher.TxnID {
		return
	}
	if uint8(higher.TxnID&1) != higherPage {
		return
	}
	p.proven = true
	p.current = higherPage
	p.hasPrevious = true
	p.previous = 1 - higherPage
}

// Proven reports whether the generation order was proven.
func (p RecoveryMetaPair) Proven() bool { return p.proven }

// Current returns the proven current page number.
func (p RecoveryMetaPair) Current() uint8 { return p.current }

// HasPrevious reports whether the proven pair carries a previous
// generation.
func (p RecoveryMetaPair) HasPrevious() bool { return p.hasPrevious }

// Previous returns the proven previous page number.
func (p RecoveryMetaPair) Previous() uint8 { return p.previous }

// StateAt returns the classified state of one meta page.
func (p RecoveryMetaPair) StateAt(page uint8) (RecoveryMetaState, bool) {
	if page > 1 {
		return RecoveryMetaState{}, false
	}
	return p.states[page], p.has[page]
}

// CurrentRecoveryMeta returns the recovery-valid meta of the proven
// current page with its page number (Rust
// ClassifiedMetas::current_recovery_meta).
func (p RecoveryMetaPair) CurrentRecoveryMeta() (page uint8, meta format.Meta, ok bool) {
	if !p.proven {
		return 0, format.Meta{}, false
	}
	state, present := p.StateAt(p.current)
	if !present || !state.RecoveryValid {
		return 0, format.Meta{}, false
	}
	return p.current, state.Recovery, true
}

// SelectedMeta returns the recovery-valid meta named by one matching
// newest token (Rust ClassifiedMetas::selected_meta over the projected
// Newest candidate: the pair must be proven, the token must name the
// current page, and the recovery meta must equal the token identity).
func (p RecoveryMetaPair) SelectedMeta(token RecoveryCandidateToken) (format.Meta, bool) {
	page, meta, ok := p.CurrentRecoveryMeta()
	if !ok || page != token.MetaPage {
		return format.Meta{}, false
	}
	if meta.DatabaseID != token.DatabaseID ||
		meta.TxnID != token.TransactionID ||
		meta.CommitNonce != token.CommitNonce {
		return format.Meta{}, false
	}
	return meta, true
}

// staticIdentityEqual compares the five static identity fields (Rust
// static_identity_eq).
func staticIdentityEqual(a, b format.Meta) bool {
	return a.AddressFamily == b.AddressFamily &&
		a.ValueKind == b.ValueKind &&
		a.StructureKind == b.StructureKind &&
		a.ValueTag == b.ValueTag &&
		a.DatabaseID == b.DatabaseID
}

// metaEqual compares the full decoded meta (Rust MetaV4 equality;
// ParseIdentity guarantees identical raw identity bytes, so the
// decoded scalars determine equality).
func metaEqual(a, b format.Meta) bool { return a == b }
