// Portable private publication-output discovery and exact removal
// (Rust publication/maintenance/output.rs): stable no-follow listing
// with tuple and digest evidence when the content is a readable v4
// main, and exact removal under the main lifetime lock.

package publication

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// maintenancePublicationTemp is the publication-temp artifact family
// (Rust output.rs ARTIFACT).
var maintenancePublicationTemp = maintenanceArtifact{
	prefix:              outputPrefix,
	invalidName:         "invalid publication temp name",
	unsupportedIdentity: "unsupported publication identity kind",
	invalidIdentity:     "invalid publication identity",
	ownershipMismatch:   "publication temp identity or link count changed",
	ownershipChanged:    "publication temp ownership changed",
	lostName:            "publication temp lost its exact name",
	remainedLinked:      "publication temp remained linked after removal",
}

// inspectAbandonedPublicationTemp inspects one exact output name
// with the optional readable content evidence (Rust output.rs
// inspect).
func inspectAbandonedPublicationTemp(dir *live.Directory, directoryIdentity LocalFileIdentity, bytes []byte, attempt [16]byte, check func() error) (*abandonedPublicationTempEntry, error) {
	identity, evidence, ok, err := inspectStable(&maintenancePublicationTemp, dir, bytes, func(file *os.File, _ live.FileIdentity) (*publicationOutputEvidence, error) {
		return contentEvidence(file, check)
	})
	if err != nil || !ok {
		return nil, err
	}
	var tuple *residueTuple
	var digest *residueDigest
	if evidence != nil {
		tuple = &evidence.tuple
		digest = &evidence.digest
	}
	return &abandonedPublicationTempEntry{
		directoryIdentity: directoryIdentity,
		artifactIdentity:  residueLocalIdentity(&identity),
		attempt:           attempt,
		tuple:             tuple,
		digest:            digest,
	}, nil
}

// contentEvidence reads the tuple and digest of one open output file
// when it has valid v4 geometry (Rust output.rs content_evidence: a
// non-v4 or unreadable meta pair carries no evidence, a malformed
// file geometry carries none, and the mapping closes here).
func contentEvidence(file *os.File, check func() error) (*publicationOutputEvidence, error) {
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	byteLength, err := fstatSize(file)
	if err != nil {
		return nil, sdkProblem(err)
	}
	if !reservationGeometryValid(byteLength) {
		return nil, nil
	}
	mapped, err := mapping.MapFile(file, byteLength, false)
	if err != nil {
		return nil, err
	}
	defer mapped.Close()
	page0, err := mapped.Page(0)
	if err != nil {
		return nil, err
	}
	page1, err := mapped.Page(1)
	if err != nil {
		return nil, err
	}
	meta, err := bootstrap.OpenMeta(page0, page1, byteLength, bootstrap.ModeImmutableReader)
	if err != nil {
		if maintenanceFormatClass(err) {
			return nil, nil
		}
		return nil, err
	}
	sum, err := digestCancellable(mapped, byteLength, check)
	if err != nil {
		// Rust folds the finished-meta/length arms into the
		// changed-while-hashing conflict; the Go digest re-checks the
		// mapping size on every view, so its errors are the sdk
		// class and pass through unchanged.
		return nil, err
	}
	return &publicationOutputEvidence{
		tuple: residueTuple{
			databaseID:    meta.DatabaseID,
			transactionID: meta.TxnID,
			commitNonce:   meta.CommitNonce,
		},
		digest: residueDigest{byteLength: byteLength, sha512: sum},
	}, nil
}

// maintenanceFormatClass classifies one bootstrap refusal as the
// non-v4 evidence marker (Rust content_evidence: Error::Format and
// Error::Corrupt both carry ErrorCode::FormatInvalid; anything else
// propagates).
func maintenanceFormatClass(err error) bool {
	var fe *format.Error
	return errors.As(err, &fe) && fe.Code == format.CodeFormatInvalid
}
