//go:build windows

package publication

// Known-answer reservation CRC vectors of the windows kind layout (Rust
// namespace/windows.rs): basename encoding 2, creator-only security 2,
// local identity 2. The three kind fields are part of the CRC-covered
// page, so the vectors differ from the unix layout; they are computed
// by the same independent reflected-Castagnoli implementation over the
// kind-2 page (the unix vectors in the posix peer validate the
// computation byte-exactly against the wire).

func reservationKnownAnswerCRCs() []reservationKnownAnswerCRC {
	return []reservationKnownAnswerCRC{
		{reservationPolicyFailIfExists, 0xe97c6457},
		{reservationPolicyReplaceExisting, 0x318f991f},
		{reservationPolicyReplaceExistingNoRollback, 0x3f93c600},
	}
}

func reservationState2KnownAnswerCRC() uint32 { return 0x07d96d91 }
