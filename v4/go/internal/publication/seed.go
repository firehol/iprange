//go:build !windows

// Publication result seed (Rust publication/result.rs Seed + NameSlot).
// The seed captures every portable fact of one prepared publication
// attempt exactly once, before any namespace removal; cleanup and the
// publish state machine draw exact artifact facts from it, and every
// artifact name slot is consumed at most once (Rust take_name expect).

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// nameSlot is the one artifact-name slot of a publication result seed
// (Rust result.rs NameSlot).
type nameSlot uint8

const (
	nameSlotPrivateOutput nameSlot = iota
	nameSlotPrivateReservation
	nameSlotCoordination
)

// seedNames is the one-shot name inventory of one publication attempt
// (Rust result.rs Names): every slot starts set and each is consumed
// exactly once by an artifact record.
type seedNames struct {
	privateOutput      []byte
	privateReservation []byte
	coordination       []byte
}

// seed is the captured portable fact inventory of one prepared
// publication attempt (Rust result.rs Seed). The inventory is
// consumed exactly once by the publication result or the preparation
// failure; cleanup draws its ledger artifact facts from it.
type seed struct {
	databaseID            [16]byte
	transactionID         uint64
	commitNonce           [16]byte
	attemptID             [16]byte
	directoryIdentity     LocalFileIdentity
	destinationBasename   []byte
	outputIdentity        LocalFileIdentity
	outputByteLength      uint64
	outputSHA512          [64]byte
	publicationPolicy     PublicationPolicy
	previousDestination   *PreviousDestination
	creationSecurity      CreationSecurity
	privateOutputBasename []byte
	names                 seedNames
}

// captureSeed snapshots one prepared output (Rust Seed::capture). The
// reservation name of a prepared attempt is an invariant of the
// machine; a failure to derive it panics exactly like the Rust expect.
func captureSeed(output *preparedOutput) seed {
	d := output.attempt.destinationOf()
	reservation, err := d.reservationName(output.attempt.attemptIDOf())
	if err != nil {
		panic("prepared attempt has a valid reservation name")
	}
	outputIdentity := output.attempt.identityOf()
	var previous *PreviousDestination
	if output.previous != nil {
		previousIdentity := localIdentityFromDeviceInode(live.IdentityDeviceInode(&output.previous.identity))
		previous = &PreviousDestination{
			Identity:   previousIdentity,
			ByteLength: output.previous.byteLength,
			SHA512:     output.previous.sha512,
		}
	}
	mainName := d.mainName()
	name := output.attempt.nameOf()
	return seed{
		databaseID:            output.meta.DatabaseID,
		transactionID:         output.meta.TxnID,
		commitNonce:           output.meta.CommitNonce,
		attemptID:             output.attempt.attemptIDOf(),
		directoryIdentity:     directoryLocalIdentity(d),
		destinationBasename:   []byte(mainName),
		outputIdentity:        localIdentityFromDeviceInode(live.IdentityDeviceInode(&outputIdentity)),
		outputByteLength:      output.byteLength,
		outputSHA512:          output.sha512,
		publicationPolicy:     publicPolicy(output.policy),
		previousDestination:   previous,
		creationSecurity:      CreationSecurity{Kind: creationSecurityKind, Commitment: d.securityCommitment()},
		privateOutputBasename: []byte(name),
		names: seedNames{
			privateOutput:      []byte(name),
			privateReservation: []byte(reservation),
			coordination:       []byte(d.coordinationName()),
		},
	}
}

// artifact builds one exact cleanup artifact of the seed (Rust
// Seed::artifact): the basename leaves its one-shot slot and the
// portable identity and creation-security facts fold in. identity is
// absent when the removal never established the inode identity; the
// portable-identity pointer of the shared artifact shape stays on the
// failure path only, exactly where the ledger is pushed.
func (s *seed) artifact(kind ArtifactKind, slot nameSlot, identity identityOptional, problem error) CleanupArtifact {
	var local *LocalFileIdentity
	if identity.present {
		converted := localIdentityFromDeviceInode(live.IdentityDeviceInode(&identity.identity))
		local = &converted
	}
	return CleanupArtifact{
		Kind:              kind,
		DirectoryRole:     DirectoryRoleDestination,
		DirectoryIdentity: s.directoryIdentity,
		BasenameEncoding:  basenameEncodingKind,
		Basename:          s.takeName(slot),
		Identity:          local,
		CreationSecurity:  &s.creationSecurity,
		UnpublishedTail:   nil,
		Error:             problem,
	}
}

// takeName consumes one artifact name slot (Rust Seed::take_name);
// consuming a slot twice panics exactly like the Rust expect.
func (s *seed) takeName(slot nameSlot) []byte {
	var name *[]byte
	switch slot {
	case nameSlotPrivateOutput:
		name = &s.names.privateOutput
	case nameSlotPrivateReservation:
		name = &s.names.privateReservation
	case nameSlotCoordination:
		name = &s.names.coordination
	}
	if *name == nil {
		panic("each artifact name is consumed once")
	}
	taken := *name
	*name = nil
	return taken
}

// publicPolicy maps the wire reservation policy to the portable
// publication policy (Rust result.rs public_policy).
func publicPolicy(policy reservationPolicy) PublicationPolicy {
	switch policy {
	case reservationPolicyFailIfExists:
		return PolicyFailIfExists
	case reservationPolicyReplaceExisting:
		return PolicyReplaceExisting
	default:
		return PolicyReplaceExistingNoRollback
	}
}
