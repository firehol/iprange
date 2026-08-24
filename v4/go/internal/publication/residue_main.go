//go:build !windows

// Retained, read-only evidence for the destination main during
// residue removal (Rust publication/residue/main.rs): the main is
// locked for its lifetime, its name is re-proved, the meta pair is
// read into the portable tuple when the geometry is valid, and the
// whole file is hashed without ever validating its graph.

package publication

import (
	"crypto/sha512"
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// residueMainGuard retains one inspected destination main (Rust
// residue::main::Guard).
type residueMainGuard struct {
	file       *os.File
	mapping    *mapping.Mapping
	identity   live.FileIdentity
	byteLength uint64
	evidence   residueMain
}

// inspectMainResidue inspects the destination main without changing
// it (Rust residue::main::inspect). A zero-length main carries no
// mapping and the empty digest, like every other Go publication
// inspection surface.
func inspectMainResidue(destination *destination, check func() error) (*residueMainGuard, error) {
	regular, err := destination.directory().OpenRegular(destination.mainName(), true)
	if err != nil {
		return nil, namespaceProblem(err)
	}
	if regular == nil {
		return nil, nil
	}
	if err := live.LockFileCancellable(regular.File, live.MainLifetimeOffset, live.LockExclusive, check); err != nil {
		_ = regular.File.Close()
		return nil, sdkProblem(err)
	}
	if err := destination.directory().VerifyName(destination.mainName(), regular.Identity); err != nil {
		_ = regular.File.Close()
		return nil, namespaceProblem(err)
	}
	byteLength, err := fstatSize(regular.File)
	if err != nil {
		_ = regular.File.Close()
		return nil, sdkProblem(err)
	}
	var mapped *mapping.Mapping
	var digest [64]byte
	if byteLength == 0 {
		digest = sha512.Sum512(nil)
	} else {
		mapped, err = mapping.MapFile(regular.File, byteLength, false)
		if err != nil {
			_ = regular.File.Close()
			return nil, sdkProblem(err)
		}
		digest, err = digestCancellable(mapped, byteLength, check)
		if err != nil {
			_ = mapped.Close()
			_ = regular.File.Close()
			return nil, outputProblem(err)
		}
	}
	tuple, err := readResidueTuple(mapped, byteLength)
	if err != nil {
		if mapped != nil {
			_ = mapped.Close()
		}
		_ = regular.File.Close()
		return nil, err
	}
	accessPolicy := AccessPolicyChangedOrUnproven
	if _, err := security.CreatorOnlyCommitment(regular.File); err == nil {
		accessPolicy = AccessPolicyCreatorOnly
	}
	identity := residueLocalIdentity(&regular.Identity)
	content := residueMainContentOther
	if tuple != nil {
		content = residueMainContentV4
	}
	return &residueMainGuard{
		file:       regular.File,
		mapping:    mapped,
		identity:   regular.Identity,
		byteLength: byteLength,
		evidence: residueMain{
			identity:     identity,
			content:      content,
			tuple:        tuple,
			digest:       residueDigest{byteLength: byteLength, sha512: digest},
			accessPolicy: accessPolicy,
		},
	}, nil
}

// Close releases the mapped view and the retained main descriptor
// (Rust drops the guard when the removal finishes; incomplete retries
// keep the guard until the retry terminal).
func (g *residueMainGuard) Close() {
	if g.mapping != nil {
		_ = g.mapping.Close()
		g.mapping = nil
	}
	if g.file != nil {
		_ = g.file.Close()
		g.file = nil
	}
}

// verify re-proves one retained main unchanged (Rust
// residue::main::Guard::verify).
func (g *residueMainGuard) verify(destination *destination) error {
	if err := destination.directory().VerifyName(destination.mainName(), g.identity); err != nil {
		return namespaceProblem(err)
	}
	if g.mapping != nil && g.mapping.Size() != g.byteLength {
		return cleanupConflictProblem("destination main length changed during removal")
	}
	length, err := fstatSize(g.file)
	if err != nil {
		return sdkProblem(err)
	}
	if length != g.byteLength {
		return cleanupConflictProblem("destination main length changed during removal")
	}
	return nil
}

// readResidueTuple reads the portable meta identity when the main has
// valid v4 geometry and a readable meta pair (Rust
// residue::main::read_tuple: page faults propagate as sdk problems,
// an unreadable meta pair stays absent).
func readResidueTuple(mapped *mapping.Mapping, byteLength uint64) (*residueTuple, error) {
	if mapped == nil || !reservationGeometryValid(byteLength) {
		return nil, nil
	}
	page0, err := mapped.Page(0)
	if err != nil {
		return nil, sdkProblem(err)
	}
	page1, err := mapped.Page(1)
	if err != nil {
		return nil, sdkProblem(err)
	}
	meta, err := bootstrap.OpenMeta(page0, page1, byteLength, bootstrap.ModeImmutableReader)
	if err != nil {
		return nil, nil
	}
	return &residueTuple{
		databaseID:    meta.DatabaseID,
		transactionID: meta.TxnID,
		commitNonce:   meta.CommitNonce,
	}, nil
}
