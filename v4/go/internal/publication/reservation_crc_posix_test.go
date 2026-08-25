//go:build !windows

package publication

// Known-answer reservation CRC vectors of the unix kind layout (Rust
// namespace/unix.rs): basename encoding 1, creator-only security 1,
// local identity 1. The values are computed by an independent
// reflected-Castagnoli implementation over the full page with the CRC
// field treated as zero (the same byte-exact layout the wire requires);
// the windows peer carries the kind-2 vectors.

func reservationKnownAnswerCRCs() []reservationKnownAnswerCRC {
	return []reservationKnownAnswerCRC{
		{reservationPolicyFailIfExists, 0x7bf19b18},
		{reservationPolicyReplaceExisting, 0xa3026650},
		{reservationPolicyReplaceExistingNoRollback, 0xad1e394f},
	}
}

func reservationState2KnownAnswerCRC() uint32 { return 0x955492de }
