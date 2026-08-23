//go:build !windows

// Stable two-inode inspection for replacement resolution (Rust
// publication/replacement_inspection.rs): the main and the private
// output open, the exclusive lifetime locks take the exact role
// order (recorded output identity, recorded previous identity, then
// the remaining entries), and each entry is finished with the meta
// pair, digest, classification, access proof, and double stability
// verification. The Rust gc_barrier availability calls are
// #[cfg(windows)] and absent here like every other Go publication
// surface.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// replacementContent classifies one replacement inspection entry
// (Rust replacement_inspection::Content).
type replacementContent uint8

const (
	replacementContentDesired replacementContent = iota
	replacementContentPrevious
	replacementContentOther
)

// inspectedReplacement is one open replacement candidate (Rust
// replacement_inspection::Inspected). The mapping and meta are
// absent until the entry is finished. Close releases the mapped view
// and the descriptor exactly where Rust drops the value.
type inspectedReplacement struct {
	name        string
	file        *os.File
	mapping     *mapping.Mapping
	identity    live.FileIdentity
	meta        format.Meta
	metaPresent bool
	byteLength  uint64
	sha512      [64]byte
	content     replacementContent
	location    outputLocation
	access      AccessPolicy
	attemptID   [16]byte
	locked      bool
}

// Close releases the inspected replacement resources (Rust drop).
func (o *inspectedReplacement) Close() error {
	var first error
	if o.mapping != nil {
		if err := o.mapping.Close(); err != nil && first == nil {
			first = err
		}
	}
	if o.file != nil {
		if err := o.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// replacementPair is the inspected main and private output pair of
// one replacement resolution (Rust replacement_inspection::Pair).
type replacementPair struct {
	main    *inspectedReplacement
	private *inspectedReplacement
}

// inspectReplacementPair runs the two-inode replacement inspection
// (Rust inspect): both candidates open, the exclusive lifetime locks
// take the exact recorded-identity roles in order, the remaining
// entries lock, and both entries finish with their exact facts.
func inspectReplacementPair(destination *destination, header reservationHeader, check func() error) (replacementPair, error) {
	privateName, err := destination.outputName(header.attemptID)
	if err != nil {
		return replacementPair{}, resolverProblem(err)
	}
	main, err := openReplacementEntry(destination, destination.mainName(), outputLocationMain, header.attemptID)
	if err != nil {
		return replacementPair{}, err
	}
	private, err := openReplacementEntry(destination, privateName, outputLocationPrivate, header.attemptID)
	if err != nil {
		if main != nil {
			_ = main.Close()
		}
		return replacementPair{}, err
	}
	if !header.previousPresent {
		if main != nil {
			_ = main.Close()
		}
		if private != nil {
			_ = private.Close()
		}
		return replacementPair{}, conflictProblem("replacement inspection requires previous evidence")
	}
	if err := lockReplacementRole(main, private, header.outputIdentity, check); err != nil {
		main.closeIfNonNil()
		private.closeIfNonNil()
		return replacementPair{}, err
	}
	if err := lockReplacementRole(main, private, header.previous.identity, check); err != nil {
		main.closeIfNonNil()
		private.closeIfNonNil()
		return replacementPair{}, err
	}
	if err := lockReplacementRemaining(main, check); err != nil {
		main.closeIfNonNil()
		private.closeIfNonNil()
		return replacementPair{}, err
	}
	if err := lockReplacementRemaining(private, check); err != nil {
		main.closeIfNonNil()
		private.closeIfNonNil()
		return replacementPair{}, err
	}
	finishedMain, err := finishReplacementEntry(destination, header, main, check)
	if err != nil {
		private.closeIfNonNil()
		return replacementPair{}, err
	}
	finishedPrivate, err := finishReplacementEntry(destination, header, private, check)
	if err != nil {
		finishedMain.closeIfNonNil()
		return replacementPair{}, err
	}
	return replacementPair{main: finishedMain, private: finishedPrivate}, nil
}

// closeIfNonNil releases one optional inspected replacement.
func (o *inspectedReplacement) closeIfNonNil() {
	if o != nil {
		_ = o.Close()
	}
}

// openReplacementEntry opens one candidate without following symlinks
// (Rust replacement_inspection::open: the single-link regular rule).
func openReplacementEntry(destination *destination, name string, location outputLocation, attemptID [16]byte) (*inspectedReplacement, error) {
	regular, err := destination.directory().OpenRegular(name, true)
	if err != nil {
		return nil, resolverProblem(err)
	}
	if regular == nil {
		return nil, nil
	}
	return &inspectedReplacement{
		name:      name,
		file:      regular.File,
		identity:  regular.Identity,
		content:   replacementContentOther,
		location:  location,
		access:    AccessPolicyUnclassified,
		attemptID: attemptID,
	}, nil
}

// lockReplacementRole takes the exclusive lifetime lock of the entry
// whose identity matches one recorded payload (Rust lock_role); an
// entry that already raced into the other role waits for its own
// turn.
func lockReplacementRole(main, private *inspectedReplacement, identity [32]byte, check func() error) error {
	entry := replacementEntryWithIdentity(main, private, identity)
	if entry == nil {
		return nil
	}
	return lockReplacementEntry(entry, check)
}

// replacementEntryWithIdentity selects the entry whose identity
// matches one recorded payload (Rust entry_with_identity; the main
// wins the tie like Rust, and the private can only carry an identity
// the main does not have).
func replacementEntryWithIdentity(main, private *inspectedReplacement, identity [32]byte) *inspectedReplacement {
	if main != nil && reservationIdentityBytes(main.identity) == identity {
		return main
	}
	if private != nil && reservationIdentityBytes(private.identity) == identity {
		return private
	}
	return nil
}

// lockReplacementRemaining takes the exclusive lifetime lock of one
// entry when its role never matched a recorded identity (Rust
// lock_remaining).
func lockReplacementRemaining(entry *inspectedReplacement, check func() error) error {
	if entry == nil || entry.locked {
		return nil
	}
	return lockReplacementEntry(entry, check)
}

// lockReplacementEntry takes one exclusive lifetime lock (Rust
// lock; the MAIN_LIFETIME_LOCK OFD range is shared with the
// publication owner).
func lockReplacementEntry(entry *inspectedReplacement, check func() error) error {
	if err := live.LockFileCancellable(entry.file, live.MainLifetimeOffset, live.LockExclusive, check); err != nil {
		return resolverProblem(err)
	}
	entry.locked = true
	return nil
}

// finishReplacementEntry completes one open entry (Rust
// replacement_inspection::finish).
func finishReplacementEntry(destination *destination, header reservationHeader, entry *inspectedReplacement, check func() error) (*inspectedReplacement, error) {
	if entry == nil {
		return nil, nil
	}
	return inspectOneReplacement(destination, header, entry, check)
}

// inspectOneReplacement runs the finished inspection of one entry
// (Rust inspect_one): the name proof, the read-only mapping, the
// digest, the desired meta derivation, the classification, the
// access proof, and the stability double-check.
func inspectOneReplacement(destination *destination, header reservationHeader, entry *inspectedReplacement, check func() error) (*inspectedReplacement, error) {
	if err := destination.directory().VerifyName(entry.name, entry.identity); err != nil {
		_ = entry.Close()
		return nil, resolverProblem(err)
	}
	byteLength, err := fstatSize(entry.file)
	if err != nil {
		_ = entry.Close()
		return nil, resolverProblem(err)
	}
	mapped, err := mapping.MapFile(entry.file, byteLength, false)
	if err != nil {
		_ = entry.Close()
		return nil, resolverProblem(err)
	}
	sha512, err := digestCancellable(mapped, byteLength, check)
	if err != nil {
		_ = mapped.Close()
		_ = entry.Close()
		return nil, outputProblem(err)
	}
	meta, metaPresent, err := desiredReplacementMeta(mapped, byteLength, sha512, header)
	if err != nil {
		_ = mapped.Close()
		_ = entry.Close()
		return nil, err
	}
	entry.mapping = mapped
	entry.byteLength = byteLength
	entry.sha512 = sha512
	entry.meta = meta
	entry.metaPresent = metaPresent
	entry.content = classifyReplacementEntry(entry, header)
	entry.access = classifyReplacementAccess(entry, header)
	if err := verifyStableReplacement(destination, entry); err != nil {
		_ = entry.Close()
		return nil, err
	}
	return entry, nil
}

// desiredReplacementMeta derives the selected meta of one entry only
// when its length and digest match the record and the meta identity
// matches (Rust desired_meta: everything else stays absent).
func desiredReplacementMeta(mapped *mapping.Mapping, byteLength uint64, sha512 [64]byte, header reservationHeader) (format.Meta, bool, error) {
	if byteLength != header.outputByteLength || sha512 != header.outputSHA512 {
		return format.Meta{}, false, nil
	}
	if !reservationGeometryValid(byteLength) {
		return format.Meta{}, false, nil
	}
	page0, err := mapped.Page(0)
	if err != nil {
		return format.Meta{}, false, resolverProblem(err)
	}
	page1, err := mapped.Page(1)
	if err != nil {
		return format.Meta{}, false, resolverProblem(err)
	}
	meta, err := bootstrap.OpenMeta(page0, page1, byteLength, bootstrap.ModeImmutableReader)
	if err != nil {
		return format.Meta{}, false, nil
	}
	if meta.DatabaseID != header.databaseID || meta.TxnID != header.transactionID || meta.CommitNonce != header.commitNonce {
		return format.Meta{}, false, nil
	}
	return meta, true, nil
}

// classifyReplacementEntry classifies one finished entry (Rust
// classify: the selected meta is Desired, an exact previous-evidence
// match is Previous, everything else is Other).
func classifyReplacementEntry(entry *inspectedReplacement, header reservationHeader) replacementContent {
	if entry.metaPresent {
		return replacementContentDesired
	}
	if !header.previousPresent {
		return replacementContentOther
	}
	if reservationIdentityBytes(entry.identity) == header.previous.identity &&
		entry.byteLength == header.previous.byteLength &&
		entry.sha512 == header.previous.sha512 {
		return replacementContentPrevious
	}
	return replacementContentOther
}

// classifyReplacementAccess derives the creator-only policy of one
// entry (Rust access).
func classifyReplacementAccess(entry *inspectedReplacement, header reservationHeader) AccessPolicy {
	commitment, err := security.CreatorOnlyCommitment(entry.file)
	if err == nil && commitment == header.securityCommitment {
		return AccessPolicyCreatorOnly
	}
	return AccessPolicyChangedOrUnproven
}

// verifyStableReplacement proves one entry stable (Rust
// verify_stable: directory verify, name verify, and the unchanged
// length).
func verifyStableReplacement(destination *destination, entry *inspectedReplacement) error {
	if err := destination.directory().Verify(); err != nil {
		return resolverProblem(err)
	}
	if err := destination.directory().VerifyName(entry.name, entry.identity); err != nil {
		return resolverProblem(err)
	}
	byteLength, err := fstatSize(entry.file)
	if err != nil {
		return resolverProblem(err)
	}
	if byteLength != entry.byteLength {
		return conflictProblem("replacement content changed while hashing")
	}
	return nil
}

// verify re-proves one finished entry with one fresh digest pass
// (Rust Inspected::verify: the stability double-check, the rehash,
// and the digest equality).
func (o *inspectedReplacement) verify(destination *destination, check func() error) error {
	if o.mapping == nil {
		return conflictProblem("finished replacement inspection retains its mapping")
	}
	if err := verifyStableReplacement(destination, o); err != nil {
		return err
	}
	digest, err := digestCancellable(o.mapping, o.byteLength, check)
	if err != nil {
		return outputProblem(err)
	}
	if err := verifyStableReplacement(destination, o); err != nil {
		return err
	}
	if digest != o.sha512 {
		return conflictProblem("replacement content changed after inspection")
	}
	return nil
}
