//go:build !windows

// Caller-result binding and header reconstruction for the resolver
// (Rust result.rs require_result_binding + PublicationResult::
// header_for): the supplied publication result is proven to belong to
// the bound destination, and its portable facts fold back into the
// authoritative reservation record shape.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// resultHeaderFor rebuilds the authoritative reservation header of
// one caller publication result (Rust header_for).
func resultHeaderFor(result *PublicationResult, destination *destination) (reservationHeader, error) {
	if err := requireResultBinding(result, destination); err != nil {
		return reservationHeader{}, err
	}
	state := reservationStatePrepared
	if result.MainNamespaceMayHaveBeenAttempted {
		state = reservationStateMainMayHaveBeenAttempted
	}
	var previous reservationPrevious
	previousPresent := false
	if result.Attempt.PreviousDestination != nil {
		previous = reservationPrevious{
			identity:   result.Attempt.PreviousDestination.Identity.Bytes,
			byteLength: result.Attempt.PreviousDestination.ByteLength,
			sha512:     result.Attempt.PreviousDestination.SHA512,
		}
		previousPresent = true
	}
	return reservationHeader{
		state:               state,
		databaseID:          result.Attempt.DatabaseID,
		transactionID:       result.Attempt.TransactionID,
		commitNonce:         result.Attempt.CommitNonce,
		attemptID:           result.Attempt.PublicationAttemptID,
		reservationIdentity: result.Attempt.ReservationIdentity.Bytes,
		policy:              policyOf(result.Attempt.PublicationPolicy),
		outputByteLength:    result.Attempt.OutputByteLength,
		outputIdentity:      result.Attempt.OutputIdentity.Bytes,
		outputSHA512:        result.Attempt.OutputSHA512,
		previous:            previous,
		previousPresent:     previousPresent,
		basenameLen:         uint32(len(destination.mainName())),
		basenameCommitment:  destination.basenameCommitmentValue(),
		securityCommitment:  result.Attempt.CreationSecurity.Commitment,
		sequence:            uint64(state),
	}, nil
}

// requireResultBinding proves one caller result belongs to the bound
// destination (Rust require_result_binding: directory identity,
// destination basename encoding and bytes, and identity kinds).
func requireResultBinding(result *PublicationResult, destination *destination) error {
	if result.Attempt.DirectoryIdentity != directoryLocalIdentity(destination) {
		return problem(format.CodeDirectoryIdentityMismatch, "caller publication result belongs to another directory")
	}
	if result.Attempt.DestinationBasenameEncoding != basenameEncodingKind ||
		string(result.Attempt.DestinationBasename) != destination.mainName() {
		return problem(format.CodeDestinationNameMismatch, "caller publication result belongs to another destination name")
	}
	if result.Attempt.OutputIdentity.Kind != identityKind ||
		result.Attempt.ReservationIdentity.Kind != identityKind ||
		result.Attempt.CreationSecurity.Kind != creationSecurityKind ||
		result.Attempt.PreviousDestination != nil && result.Attempt.PreviousDestination.Identity.Kind != identityKind {
		return conflictProblem("caller publication result has another identity kind")
	}
	return nil
}

// policyOf maps one public publication policy back to the internal
// reservation policy (Rust header_for policy match).
func policyOf(policy PublicationPolicy) reservationPolicy {
	switch policy {
	case PolicyReplaceExisting:
		return reservationPolicyReplaceExisting
	case PolicyReplaceExistingNoRollback:
		return reservationPolicyReplaceExistingNoRollback
	default:
		return reservationPolicyFailIfExists
	}
}
