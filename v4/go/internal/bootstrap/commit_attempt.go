// Exact two-page commit-attempt classification (Rust bootstrap.rs
// resolve_commit_attempt + CommitAttemptResolution): the commit
// resolution owner classifies one attempted transaction and nonce
// against both decoded meta pages after the writer-mode pair
// selection, without validating either page graph.

package bootstrap

import "github.com/firehol/iprange/v4/go/internal/format"

// CommitAttemptResolution is the exact outcome of one attempted
// transaction and nonce against the selected meta pair (Rust
// CommitAttemptResolution).
type CommitAttemptResolution uint8

const (
	CommitAttemptCommitted CommitAttemptResolution = iota
	CommitAttemptNotCommitted
	CommitAttemptSupersededUnknown
)

// ResolveCommitAttempt classifies one attempted transaction and nonce
// against both meta pages (Rust resolve_commit_attempt): the committed
// pair is selected first with the writer rule, the selected generation
// must belong to the attempted database, and then each exact meta
// identity is checked in the Rust order - an exact transaction+nonce
// match is Committed, an exact transaction with a different nonce or a
// selected generation older than the attempt is NotCommitted, a
// selected generation newer than the attempt is SupersededUnknown, and
// anything else cannot prove an outcome.
func ResolveCommitAttempt(p0, p1 []byte, physical uint64, databaseID [16]byte, transactionID uint64, commitNonce [16]byte) (CommitAttemptResolution, error) {
	selected, err := openMetaPages(p0, p1, physical, ModeWriter)
	if err != nil {
		return 0, err
	}
	if selected.Meta.DatabaseID != databaseID {
		return 0, problemErr(ProblemStaticIdentityMismatch, "conflicting meta identity")
	}
	m0, ok0 := format.ParseIdentity(p0)
	m1, ok1 := format.ParseIdentity(p1)
	if validateMeta(m0, ok0, physical) != nil || validateMeta(m1, ok1, physical) != nil {
		return 0, problemErr(ProblemCurrentGenerationUnprovable, "current generation not provable")
	}
	if (m0.TxnID == transactionID && m0.CommitNonce == commitNonce) ||
		(m1.TxnID == transactionID && m1.CommitNonce == commitNonce) {
		return CommitAttemptCommitted, nil
	}
	if (m0.TxnID == transactionID && m0.CommitNonce != commitNonce) ||
		(m1.TxnID == transactionID && m1.CommitNonce != commitNonce) ||
		selected.Meta.TxnID < transactionID {
		return CommitAttemptNotCommitted, nil
	}
	if selected.Meta.TxnID > transactionID {
		return CommitAttemptSupersededUnknown, nil
	}
	return 0, problemErr(ProblemCurrentGenerationUnprovable, "current generation not provable")
}
