package bootstrap

// Recovery meta classification (Rust bootstrap/recovery_meta.rs plus
// the recovery/classify.rs state split): each raw meta page is
// classified twice - the ORDER state proves the identity and the
// commit identity (transaction and nonce) without any structural
// proof; the RECOVERY state adds the full recovery-valid structural
// surface (declared page count, roots, counts, metadata lengths,
// allocator reserve, and the value-kind invariants). The split is
// the recovery story: an order-valid page can still name the
// generation order while its structural defects keep it from being a
// recoverable candidate.

import "github.com/firehol/iprange/v4/go/internal/format"

// RecoveryMetaState is the two-stage classification of one raw meta
// page (Rust RecoveryMetaState: order Result + recovery Result).
// OrderValid is exactly the Rust order arm (identity-readable plus a
// nonzero transaction and commit nonce); RecoveryValid requires the
// order arm and the full ValidateKindInvariants surface. MagicOk
// reports whether the page carries the main magic, the split the Go
// validation report already uses (Magic -> MetaUnavailable, every
// other problem -> MetaInvalid).
type RecoveryMetaState struct {
	Order         format.Meta
	OrderValid    bool
	MagicOk       bool
	Recovery      format.Meta
	RecoveryValid bool
}

// ClassifyRecoveryMeta classifies one complete raw meta page (Rust
// classify_recovery_meta: identity_readable, then validate_commit_
// identity, then recovery_valid over the structural checks).
func ClassifyRecoveryMeta(page []byte) RecoveryMetaState {
	state := RecoveryMetaState{MagicOk: metaMagicValid(page)}
	meta, ok := format.ParseIdentity(page)
	if !ok {
		return state
	}
	state.Order = meta
	state.OrderValid = meta.TxnID != 0 && !allZero16(meta.CommitNonce)
	if !state.OrderValid {
		return state
	}
	state.Recovery = meta
	state.RecoveryValid = meta.ValidateKindInvariants() == nil
	return state
}

// metaMagicValid reports whether the page starts with the main magic
// (the Rust MetaProblem::Magic split used by the finding reasons).
func metaMagicValid(page []byte) bool {
	return len(page) >= len(format.MainMagic) && string(page[:len(format.MainMagic)]) == string(format.MainMagic[:])
}

// allZero16 reports whether a 16-byte value is entirely zero.
func allZero16(v [16]byte) bool {
	for _, b := range v {
		if b != 0 {
			return false
		}
	}
	return true
}
