// Package iprangeformat reads and writes the iprange v3 portable binary
// threat-intel format. It is a pure-Go implementation that is byte-identical to the
// Rust reference (rust/iprange-format); both pass the shared conformance corpus.
//
// The on-disk contract is specified in .agents/sow/specs/binary-format-v3.md.
package iprangeformat

// Format constants (normative). Each value matches binary-format-v3.md and the Rust
// spec module field-for-field; changing one is a format change.
var magic = [8]byte{'I', 'P', 'R', 'A', 'N', 'G', 'E', '3'}

const (
	versionMajor uint16 = 3
	versionMinor uint16 = 0
	// versionMinorMerged is v3.1: a multi-feed merged file (§13). version_minor==1 ⟺
	// a catalog section is present ⟺ the file is merged (§13.1).
	versionMinorMerged uint16 = 1

	headerSize        = 72 // fixed for v3.0
	dirEntrySize      = 72
	indexSubHeaderLen = 32

	v4RecordSize = 12
	v6RecordSize = 40

	// valueIDNone marks "present, no value"; caps the value table at 2^32-2 entries.
	valueIDNone uint32 = 0xFFFF_FFFF

	feedMetaFieldCount uint32 = 6
)

// Section kinds (§8).
const (
	kindFeedMeta  uint32 = 1
	kindIndex     uint32 = 2
	kindValues    uint32 = 3
	kindCatalog   uint32 = 4 // per-feed identity catalog (merged file, v3.1 §13.2)
	kindSignature uint32 = 5
)

// Flag bits.
const (
	flagIPVersion         uint16 = 0b1 // header.flags bit0: 0=IPv4, 1=IPv6
	dirFlagMustUnderstand uint32 = 0b1 // dir_entry.flags bit0
	licenseFlagDontRedist uint32 = 0b1 // header.license_flags bit0
)

// sha256Empty is SHA-256 of the empty input — the hash of any zero-length section.
var sha256Empty = [32]byte{
	0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14, 0x9a, 0xfb, 0xf4, 0xc8, 0x99, 0x6f, 0xb9, 0x24,
	0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c, 0xa4, 0x95, 0x99, 0x1b, 0x78, 0x52, 0xb8, 0x55,
}

// kindAlign returns the canonical alignment for a known kind (§8).
func kindAlign(kind uint32) (uint64, bool) {
	switch kind {
	case kindIndex:
		return 16, true
	case kindFeedMeta, kindValues, kindCatalog, kindSignature:
		return 8, true
	default:
		return 0, false
	}
}

// kindFlags returns the canonical dir-entry flags for a known kind (§6):
// must_understand=1 for the required core sections (1/2/3), 0 for the signature (5).
func kindFlags(kind uint32) uint32 {
	switch kind {
	case kindFeedMeta, kindIndex, kindValues:
		return dirFlagMustUnderstand
	default: // signature (5) and catalog (4): must_understand = 0
		return 0
	}
}

// alignUp returns align_up(x, a) for a power-of-two a, or ok=false on overflow (§3).
func alignUp(x, a uint64) (uint64, bool) {
	m := a - 1
	if x > maxUint64-m {
		return 0, false
	}
	return (x + m) & ^m, true
}

const maxUint64 = ^uint64(0)

// isValidAlign reports whether a is a power of two in [8, 4096] (§6).
func isValidAlign(a uint64) bool {
	return a >= 8 && a <= 4096 && a&(a-1) == 0
}

// canonRank gives a section's canonical-position rank: signature (5) sorts last;
// every other kind ranks by its numeric value (the §4/§8 band order).
func canonRank(kind uint32) uint64 {
	if kind == kindSignature {
		return maxUint64
	}
	return uint64(kind)
}
