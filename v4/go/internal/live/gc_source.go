//go:build windows

// Exact source-name and directory-role binding for Windows GC records
// (Rust publication/gc/source.rs). Every artifact kind fixes the
// directory role and the exact source component shape; the GC record
// validity check uses these predicates so a forged envelope cannot
// claim an unrelated name.

package live

import "strings"

// gcRoleMatches reports whether one artifact kind may live in the
// recorded directory role (Rust role_matches).
func gcRoleMatches(kind ArtifactKind, role DirectoryRole) bool {
	switch kind {
	case ArtifactPrivateOutput, ArtifactPrivateReservation:
		return role == DirectoryRoleDestination
	case ArtifactOwnedCoordination:
		return role == DirectoryRoleDestination || role == DirectoryRoleMainFile
	case ArtifactAuthorizedScratch:
		return role == DirectoryRoleScratchDirectory
	case ArtifactOwnedMain:
		return role == DirectoryRoleMainFile
	}
	return false
}

// gcNameMatches reports whether one source component is the exact
// attempt-derived private name of the artifact kind (Rust
// name_matches): the private output and reservation names, the
// canonical coordination twins, the scratch name, or a valid main
// name.
func gcNameMatches(kind ArtifactKind, attemptID [16]byte, ordinal uint32, source string) bool {
	switch kind {
	case ArtifactPrivateOutput:
		name, err := gcPrivateName(gcOutputPrefix, attemptID)
		return err == nil && name == source
	case ArtifactPrivateReservation:
		name, err := gcPrivateName(gcReservationPrefix, attemptID)
		return err == nil && name == source
	case ArtifactOwnedCoordination:
		return gcCoordinationSource(source)
	case ArtifactAuthorizedScratch:
		return source == gcScratchName(attemptID, ordinal)
	case ArtifactOwnedMain:
		return gcMainSource(source)
	}
	return false
}

// gcCoordinationSource proves one coordination twin (Rust
// coordination_source): the component is <main>.readers or
// <main>.readers.reset with a valid main stem.
func gcCoordinationSource(source string) bool {
	var main string
	switch {
	case strings.HasSuffix(source, gcReadersSuffix):
		main = source[:len(source)-len(gcReadersSuffix)]
	case strings.HasSuffix(source, gcReadersResetSuffix):
		main = source[:len(source)-len(gcReadersResetSuffix)]
	default:
		return false
	}
	return gcMainSource(main)
}

// gcMainSource proves one main component (Rust main_source): the valid
// destination main-name rule.
func gcMainSource(source string) bool {
	return !mainNameComponentRule(source)
}

const (
	gcOutputPrefix       = ".iprange-publish-"
	gcReservationPrefix  = ".iprange-reservation-"
	gcPrivateSuffix      = ".tmp"
	gcReadersSuffix      = ".readers"
	gcReadersResetSuffix = ".readers.reset"
)

// gcPrivateName builds one private artifact name: prefix, 32 lowercase
// hex attempt characters, suffix (Rust private_name; no ordinal).
func gcPrivateName(prefix string, attempt [16]byte) (string, error) {
	if attempt == [16]byte{} {
		return "", nsInvalidNameError()
	}
	var buf [len(gcOutputPrefix) + 32 + len(gcPrivateSuffix)]byte
	off := copy(buf[:], prefix)
	gcHexEncode(buf[off:off+32], attempt[:])
	copy(buf[off+32:], gcPrivateSuffix)
	return string(buf[:off+32+len(gcPrivateSuffix)]), nil
}

// gcHexEncode writes 32 lowercase hex characters of one 16-byte value.
func gcHexEncode(dst, value []byte) {
	const digits = "0123456789abcdef"
	for i, b := range value {
		dst[2*i] = digits[b>>4]
		dst[2*i+1] = digits[b&0x0f]
	}
}

// gcScratchName builds one authorized scratch name (Rust
// artifact_name::scratch_name).
func gcScratchName(attemptID [16]byte, ordinal uint32) string {
	name, _ := gcName(".iprange-scratch-", attemptID, ordinal)
	return name
}

// mainNameComponentRule is the live valid-name predicate mirrored from
// the publication platform rule (Rust validate_main_name): one exact
// component without separators or NUL.
func mainNameComponentRule(name string) bool {
	return name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') ||
		strings.IndexByte(name, 0) >= 0
}
