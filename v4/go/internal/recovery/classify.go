// Package recovery implements the exact recovery-candidate surface of
// the v4 format (Rust recovery.rs): the two-stage per-page meta
// classification, the generation-order proof, the exact candidate
// tokens, and the candidate inspection and recovery machines. The
// package composes the bootstrap authority for the classification
// and the validation/publication machines for the sweep and terminal
// surfaces, mirroring the Rust recovery module layout.
package recovery

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// RecoveryCandidateLabel is the exact role of one recoverable
// retained meta page (Rust RecoveryCandidateLabel).
type RecoveryCandidateLabel uint8

const (
	// CandidateNewest is the proven current generation of a live or
	// retired pair.
	CandidateNewest RecoveryCandidateLabel = iota
	// CandidatePrevious is the proven previous generation of a live
	// pair (Rust Previous).
	CandidatePrevious
	// CandidateUnorderedMeta0 is meta page 0 of a pair whose order
	// cannot be proven (Rust UnorderedMeta0).
	CandidateUnorderedMeta0
	// CandidateUnorderedMeta1 is meta page 1 of an unordered pair
	// (Rust UnorderedMeta1).
	CandidateUnorderedMeta1
)

// String returns the wire label of one candidate.
func (l RecoveryCandidateLabel) String() string {
	switch l {
	case CandidateNewest:
		return "newest"
	case CandidatePrevious:
		return "previous"
	case CandidateUnorderedMeta0:
		return "unordered-meta0"
	case CandidateUnorderedMeta1:
		return "unordered-meta1"
	}
	return "unknown"
}

// RecoveryCandidate is the exact opaque token of one recoverable
// retained meta page (Rust RecoveryCandidate): the label, the meta
// page, the retained source identity, and the generation identity.
type RecoveryCandidate struct {
	Label          RecoveryCandidateLabel
	MetaPage       uint8
	SourceIdentity publication.LocalFileIdentity
	DatabaseID     [16]byte
	TransactionID  uint64
	CommitNonce    [16]byte
}

// classifiedMetas is one classified meta pair (Rust ClassifiedMetas):
// the recovery label projection and progress over the bootstrap
// order authority.
type classifiedMetas struct {
	pair bootstrap.RecoveryMetaPair
}

// classifyMetas derives the order from two raw meta states (Rust
// ClassifiedMetas::new over classify_order; the order proof is the
// bootstrap authority).
func classifyMetas(states [2]bootstrap.RecoveryMetaState, has [2]bool) classifiedMetas {
	return classifiedMetas{pair: bootstrap.ClassifyRecoveryMetaPair(states, has)}
}

// stateAt returns the classified state of one meta page.
func (c *classifiedMetas) stateAt(page uint8) (bootstrap.RecoveryMetaState, bool) {
	return c.pair.StateAt(page)
}

// currentRecoveryMeta returns the recovery-valid meta of the proven
// current page (Rust ClassifiedMetas::current_recovery_meta).
func (c *classifiedMetas) currentRecoveryMeta() (format.Meta, bool) {
	_, meta, ok := c.pair.CurrentRecoveryMeta()
	return meta, ok
}

// candidates projects the recoverable candidate tokens (Rust
// ClassifiedMetas::candidates: the proven pair exposes newest then
// previous; an unordered pair exposes both pages in order; only
// recovery-valid pages become candidates).
func (c *classifiedMetas) candidates(identity publication.LocalFileIdentity) [2]*RecoveryCandidate {
	var out [2]*RecoveryCandidate
	if c.pair.Proven() {
		if candidate := c.candidate(identity, c.pair.Current(), CandidateNewest); candidate != nil {
			out[0] = candidate
		}
		if !c.pair.HasPrevious() {
			return out
		}
		slot := 1
		if out[0] == nil {
			slot = 0
		}
		if candidate := c.candidate(identity, c.pair.Previous(), CandidatePrevious); candidate != nil {
			out[slot] = candidate
		}
		return out
	}
	var slot uint8
	for page := uint8(0); page < 2; page++ {
		label := CandidateUnorderedMeta1
		if page == 0 {
			label = CandidateUnorderedMeta0
		}
		if candidate := c.candidate(identity, page, label); candidate != nil {
			out[slot] = candidate
			slot++
		}
	}
	return out
}

// candidate builds the token of one recovery-valid page (Rust
// ClassifiedMetas::candidate).
func (c *classifiedMetas) candidate(identity publication.LocalFileIdentity, page uint8, label RecoveryCandidateLabel) *RecoveryCandidate {
	state, ok := c.stateAt(page)
	if !ok || !state.RecoveryValid {
		return nil
	}
	meta := state.Recovery
	return &RecoveryCandidate{
		Label:          label,
		MetaPage:       page,
		SourceIdentity: identity,
		DatabaseID:     meta.DatabaseID,
		TransactionID:  meta.TxnID,
		CommitNonce:    meta.CommitNonce,
	}
}

// tokenMatches reports whether one token names one of the current
// candidates (Rust ClassifiedMetas::token_matches: token equality).
func (c *classifiedMetas) tokenMatches(token *RecoveryCandidate) bool {
	for _, candidate := range c.candidates(token.SourceIdentity) {
		if candidate != nil && *candidate == *token {
			return true
		}
	}
	return false
}

// selectedMeta returns the recovery-valid meta named by one matching
// token (Rust ClassifiedMetas::selected_meta).
func (c *classifiedMetas) selectedMeta(token *RecoveryCandidate) (format.Meta, bool) {
	if !c.tokenMatches(token) {
		return format.Meta{}, false
	}
	state, ok := c.stateAt(token.MetaPage)
	if !ok || !state.RecoveryValid {
		return format.Meta{}, false
	}
	return state.Recovery, true
}

// progress folds the classification findings exactly like the Rust
// ClassifiedMetas::progress: every absent page is the IoError class
// with the untraversable mark, every recovery-invalid page is one
// finding (Magic -> MetaUnavailable, every other class ->
// MetaInvalid), and a fully present recovery-valid pair with an
// unprovable order adds one more MetaInvalid finding.
func (c *classifiedMetas) progress() (validation.ValidationProgress, error) {
	var progress validation.ValidationProgress
	for page := uint8(0); page < 2; page++ {
		state, ok := c.stateAt(page)
		if !ok {
			if err := recordClassifiedProblem(&progress, true, false); err != nil {
				return progress, err
			}
			continue
		}
		if !state.RecoveryValid {
			if err := recordClassifiedProblem(&progress, false, state.MagicOk); err != nil {
				return progress, err
			}
		}
	}
	if !c.pair.Proven() {
		s0, ok0 := c.pair.StateAt(0)
		s1, ok1 := c.pair.StateAt(1)
		if ok0 && ok1 && s0.RecoveryValid && s1.RecoveryValid {
			if err := recordClassifiedProblem(&progress, false, true); err != nil {
				return progress, err
			}
		}
	}
	return progress, nil
}

// recordClassifiedProblem counts one classification finding and marks
// the untraversable subgraph (Rust record_problem + reason: Magic ->
// MetaUnavailable, every other class -> MetaInvalid; absent pages are
// the IoError class per record_recovery_problems).
func recordClassifiedProblem(progress *validation.ValidationProgress, absent bool, magicOk bool) error {
	reason := validation.ReasonMetaInvalid
	if absent {
		reason = validation.ReasonIoError
	} else if !magicOk {
		reason = validation.ReasonMetaUnavailable
	}
	if err := validation.CountFinding(progress, reason); err != nil {
		return err
	}
	return validation.MarkUntraversable(progress, true)
}
