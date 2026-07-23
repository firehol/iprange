package exactv4

import "bytes"

type processStartParseError uint8

const (
	processStartMissingCommandEnd processStartParseError = iota + 1
	processStartMissingField
	processStartInvalidNumber
	processStartOverflow
	processStartZero
)

// parseLinuxProcStatStart reads /proc/<pid>/stat field 22 after the final ')'.
// The command field may itself contain spaces and ')' bytes.
func parseLinuxProcStatStart(data []byte) (uint64, processStartParseError) {
	commandEnd := bytes.LastIndexByte(data, ')')
	if commandEnd < 0 {
		return 0, processStartMissingCommandEnd
	}
	fields := bytes.Fields(data[commandEnd+1:])
	if len(fields) <= 19 {
		return 0, processStartMissingField
	}
	return parseNonzeroUint64(fields[19])
}

type posixProcessObservationKind uint8

const (
	posixProcessMissing posixProcessObservationKind = iota + 1
	posixProcessExists
	posixProcessUncertain
)

type posixProcessObservation struct {
	kind         posixProcessObservationKind
	currentStart uint64
}

func classifyPOSIXDeath(active activeSlot, observation posixProcessObservation) (deathProof, bool) {
	switch observation.kind {
	case posixProcessMissing:
		return deathProof{kind: deathProofPOSIXMissing, processID: active.processID}, true
	case posixProcessExists:
		if active.processStart != 0 && observation.currentStart != 0 &&
			active.processStart != observation.currentStart {
			return deathProof{
				kind: deathProofPOSIXPIDReused, processID: active.processID,
				currentStart: observation.currentStart,
			}, true
		}
	}
	return deathProof{}, false
}

type windowsProcessObservationKind uint8

const (
	windowsProcessSignaled windowsProcessObservationKind = iota + 1
	windowsProcessRunning
	windowsProcessUncertain
)

type windowsProcessObservation struct {
	kind         windowsProcessObservationKind
	currentStart uint64
}

func classifyWindowsDeath(active activeSlot, observation windowsProcessObservation) (deathProof, bool) {
	switch observation.kind {
	case windowsProcessSignaled:
		return deathProof{kind: deathProofWindowsSignaled, processID: active.processID}, true
	case windowsProcessRunning:
		if active.processStart != 0 && observation.currentStart != 0 &&
			active.processStart != observation.currentStart {
			return deathProof{
				kind: deathProofWindowsPIDReused, processID: active.processID,
				currentStart: observation.currentStart,
			}, true
		}
	}
	return deathProof{}, false
}

func parseNonzeroUint64(data []byte) (uint64, processStartParseError) {
	if len(data) == 0 {
		return 0, processStartInvalidNumber
	}
	var value uint64
	for _, digit := range data {
		if digit < '0' || digit > '9' {
			return 0, processStartInvalidNumber
		}
		if value > (^uint64(0)-uint64(digit-'0'))/10 {
			return 0, processStartOverflow
		}
		value = value*10 + uint64(digit-'0')
	}
	if value == 0 {
		return 0, processStartZero
	}
	return value, 0
}
