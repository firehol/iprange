// Mechanical public-SDK-value conversion for JSON-RPC results (Rust
// handlers/convert.rs parity).

package handlers

import (
	"fmt"
	"net/netip"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// DecimalUint renders a u64 as a decimal wire string.
func DecimalUint(value uint64) string {
	return fmt.Sprintf("%d", value)
}

// HexBytes renders bytes as lowercase hex (wire digest/id form).
func HexBytes(bytes []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(bytes)*2)
	for _, b := range bytes {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

// HexID renders a 16-byte identity as lowercase hex.
func HexID(bytes *[16]byte) string {
	return HexBytes(bytes[:])
}

// ValueTagJSON renders a value tag as its wire object.
func ValueTagJSON(tag iprangedb.ValueTag) map[string]any {
	return map[string]any{"hex": HexBytes(tag.Bytes())}
}

// AddressFamilyName maps the SDK family to its wire name.
func AddressFamilyName(family iprangedb.AddressFamily) string {
	switch family {
	case iprangedb.AddressFamilyIPv4:
		return "ipv4"
	case iprangedb.AddressFamilyIPv6:
		return "ipv6"
	}
	return "unknown"
}

// ValueKindName maps the SDK value kind to its wire name.
func ValueKindName(kind iprangedb.ValueKind) string {
	switch kind {
	case iprangedb.ValueKindDirect:
		return "direct"
	case iprangedb.ValueKindMembership:
		return "membership"
	case iprangedb.ValueKindStructured:
		return "structured"
	}
	return "unknown"
}

// StructureKindName maps the SDK structure kind to its wire name.
func StructureKindName(kind iprangedb.StructureKind) string {
	switch kind {
	case iprangedb.StructureKindNone:
		return "none"
	case iprangedb.StructureKindNetworkEnrichmentV1:
		return "network_enrichment_v1"
	}
	return "unknown"
}

// MetaSelectionName maps the SDK meta selection to its wire name.
func MetaSelectionName(selection iprangedb.MetaSelection) string {
	switch selection {
	case iprangedb.MetaSelectionProvenCurrent:
		return "proven_current"
	case iprangedb.MetaSelectionSoleMeta0:
		return "sole_meta_0"
	case iprangedb.MetaSelectionSoleMeta1:
		return "sole_meta_1"
	}
	return "unknown"
}

// DatabaseInfoJSON converts the SDK database info to the wire result
// object.
func DatabaseInfoJSON(info *iprangedb.DatabaseInfo) map[string]any {
	return map[string]any{
		"address_family":     AddressFamilyName(info.Family),
		"value_kind":         ValueKindName(info.ValueKind),
		"structure_kind":     StructureKindName(info.StructureKind),
		"value_tag":          ValueTagJSON(info.ValueTag),
		"database_id":        HexID(&info.DatabaseID),
		"transaction_id":     DecimalUint(info.TransactionID),
		"commit_nonce":       HexID(&info.CommitNonce),
		"page_count":         DecimalUint(info.PageCount),
		"range_record_count": DecimalUint(info.RangeRecordCount),
		"active_feed_count":  DecimalUint(info.ActiveFeedCount),
		"meta_selection":     MetaSelectionName(info.MetaSelection),
	}
}

// LocationJSON converts one optional network-enrichment location to
// its wire value (null when absent).
func LocationJSON(location iprangedb.NetworkEnrichmentV1Location, has bool) any {
	if !has {
		return nil
	}
	return map[string]any{
		"latitude_microdegrees":  location.LatitudeMicrodegrees,
		"longitude_microdegrees": location.LongitudeMicrodegrees,
	}
}

// NetworkEnrichmentJSON converts one decoded structured payload to its
// wire object.
func NetworkEnrichmentJSON(value iprangedb.NetworkEnrichmentV1, threatFeeds []string) map[string]any {
	return map[string]any{
		"asn":          value.ASN,
		"country_id":   value.CountryID,
		"state_id":     value.StateID,
		"city_id":      value.CityID,
		"location":     LocationJSON(value.Location, value.HasLocation),
		"threat_feeds": threatFeeds,
	}
}

// CursorAddress renders one canonical cursor checkpoint address.
func CursorAddress(point rpc.CursorPoint) string {
	if point.V4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d", *point.V4>>24, (*point.V4>>16)&0xff, (*point.V4>>8)&0xff, *point.V4&0xff)
	}
	if point.V6 != nil {
		return ipv6String(*point.V6)
	}
	return ""
}

// CursorAddressV6 renders an IPv6 checkpoint address (a u128 in the
// wire format) canonically.
func CursorAddressV6(v6 *iprangedb.IPv6) string {
	if v6 == nil {
		return ""
	}
	return ipv6String(*v6)
}

// ipv6String renders the Hi/Lo halves as canonical IPv6 text.
func ipv6String(v6 iprangedb.IPv6) string {
	var bytes [16]byte
	bytes[0] = byte(v6.Hi >> 56)
	bytes[1] = byte(v6.Hi >> 48)
	bytes[2] = byte(v6.Hi >> 40)
	bytes[3] = byte(v6.Hi >> 32)
	bytes[4] = byte(v6.Hi >> 24)
	bytes[5] = byte(v6.Hi >> 16)
	bytes[6] = byte(v6.Hi >> 8)
	bytes[7] = byte(v6.Hi)
	bytes[8] = byte(v6.Lo >> 56)
	bytes[9] = byte(v6.Lo >> 48)
	bytes[10] = byte(v6.Lo >> 40)
	bytes[11] = byte(v6.Lo >> 32)
	bytes[12] = byte(v6.Lo >> 24)
	bytes[13] = byte(v6.Lo >> 16)
	bytes[14] = byte(v6.Lo >> 8)
	bytes[15] = byte(v6.Lo)
	return netip.AddrFrom16(bytes).String()
}
