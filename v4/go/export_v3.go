package iprangedb

// The v4 -> v3 snapshot bridge (§13): export a sealed, canonical v3 file from a
// validated v4 image. Mirrors the Rust export module
// (v4/rust/iprange-livedb/src/export.rs).
//
// Export does not re-implement the v3 writer rules — it opens the v4 file with the v4
// Reader (which fully validates it), scans the records in key order, maps each record's
// scope_id (u32) to a v3 Value (the 4 little-endian bytes of scope_id under type_id),
// and feeds the ordered (range, value) stream plus the caller-supplied V3Meta to the v3
// Writer. The v3 writer owns coalescing, value interning, the uint32 values-table cap,
// the 128-bit unique_ip_count accounting, and byte-identical canonicalization (§13
// export contract).
//
// v4.3 mapping (§13 step 2): every record carries a u32 scope_id. It is encoded verbatim
// as 4 little-endian bytes under the caller-supplied type_id. v4 stores no type_id; it is
// the caller's (D11). Unlike the v4.1 bridge there is no scope_width and no "present, no
// value" sentinel — a scope_id of 0 simply exports as Value{type_id, [0,0,0,0]}.
//
// Export is not total: every error the v3 writer returns from AddRange / Build (the §13
// unrepresentable cases — unique_ip_count reaches 2^128, distinct (type_id, value) pairs
// exceed the v3 cap, or a non-conforming type_id / scope) is wrapped in
// ErrExportUnrepresentable. A corrupt v4 file is a normal error (surfaced as-is).

import (
	"encoding/binary"
	"errors"
	"fmt"

	v3 "github.com/firehol/iprange/v3/go"
)

// ErrExportUnrepresentable marks a v4 state the v3 writer rejected (§13): the export is
// not representable as a v3 snapshot. Test with errors.Is. The wrapped cause is the v3
// writer's error.
var ErrExportUnrepresentable = errors.New("v4 state not representable as a v3 snapshot")

// V3Meta carries the v3 inputs v4 does not store (§13): the six feed-meta fields,
// license_flags, and generation_unixtime. Passed through to the v3 writer verbatim.
type V3Meta struct {
	FeedMeta           v3.FeedMeta
	LicenseFlags       uint32
	GenerationUnixtime uint64
}

// ExportV3 exports a validated v4 image to a sealed v3 snapshot (§13).
//
// It opens v4Bytes with the v4 Reader (full validation), scans every record in key
// order, maps each record's scope_id (u32) to a v3 value (4 LE bytes under typeID), and
// seals the v3 file with meta.
//
// On a v3-writer rejection it returns an error wrapping ErrExportUnrepresentable
// (errors.Is(err, ErrExportUnrepresentable) is true); other errors (a corrupt v4 file)
// are surfaced as-is.
func ExportV3(v4Bytes []byte, typeID uint32, meta V3Meta) ([]byte, error) {
	r, err := Open(v4Bytes)
	if err != nil {
		return nil, err
	}
	switch r.KeyWidth() {
	case 4:
		w := v3.NewWriterV4(meta.FeedMeta, meta.LicenseFlags, meta.GenerationUnixtime)
		var addErr error
		serr := r.ScanV4(func(from, to Ipv4Key, scopeID uint32) {
			if addErr != nil {
				return // already failed; ignore the rest
			}
			addErr = w.AddRange(v3.Ipv4Key(from), v3.Ipv4Key(to), scopeValue(typeID, scopeID))
		})
		if serr != nil {
			return nil, serr
		}
		if addErr != nil {
			return nil, unrepresentable(addErr)
		}
		return buildV3(w.Build())
	case 16:
		w := v3.NewWriterV6(meta.FeedMeta, meta.LicenseFlags, meta.GenerationUnixtime)
		var addErr error
		serr := r.ScanV6(func(from, to Ipv6Key, scopeID uint32) {
			if addErr != nil {
				return
			}
			addErr = w.AddRange(
				v3.Ipv6Key{Hi: from.Hi, Lo: from.Lo},
				v3.Ipv6Key{Hi: to.Hi, Lo: to.Lo},
				scopeValue(typeID, scopeID),
			)
		})
		if serr != nil {
			return nil, serr
		}
		if addErr != nil {
			return nil, unrepresentable(addErr)
		}
		return buildV3(w.Build())
	default:
		// Open already validated the image; unreachable for a well-formed file.
		return nil, fmt.Errorf("export_v3: unknown v4 key width %d", r.KeyWidth())
	}
}

// scopeValue maps a v4 record's scope_id (u32) to a v3 value (§13 step 2, v4.3): the 4
// little-endian bytes of scope_id, under typeID. Each call returns a fresh, independent
// slice (no aliasing with the v4 image), matching the Rust scope_id.to_le_bytes().to_vec().
func scopeValue(typeID uint32, scopeID uint32) *v3.Value {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], scopeID)
	return &v3.Value{TypeID: typeID, Bytes: b[:]}
}

// buildV3 finalizes the v3 writer's Build result: any v3-writer error becomes an
// ExportUnrepresentable error.
func buildV3(bytes []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, unrepresentable(err)
	}
	return bytes, nil
}

// unrepresentable wraps a v3-writer rejection as the distinct ExportUnrepresentable
// error (§13) — never leak it as a generic error.
func unrepresentable(cause error) error {
	return fmt.Errorf("%w: %v", ErrExportUnrepresentable, cause)
}
