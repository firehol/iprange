//! Mechanical public-SDK-value conversion for JSON-RPC results.

use iprange_livedb::{
    AddressFamily, DatabaseInfo, MetaSelection, NetworkEnrichmentV1, NetworkEnrichmentV1Location,
    NetworkEnrichmentV1View, StructureKind, ValueKind,
};
use serde_json::{json, Value};

use super::super::state::CursorPoint;

pub fn decimal_u64(value: u64) -> String {
    value.to_string()
}

pub(crate) fn hex_bytes(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub fn hex_id(bytes: &[u8; 16]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub fn value_tag(bytes: &[u8]) -> Value {
    json!({ "hex": bytes.iter().map(|byte| format!("{byte:02x}")).collect::<String>() })
}

pub fn address_family(value: AddressFamily) -> &'static str {
    match value {
        AddressFamily::Ipv4 => "ipv4",
        AddressFamily::Ipv6 => "ipv6",
    }
}

pub fn value_kind(value: ValueKind) -> &'static str {
    match value {
        ValueKind::Direct => "direct",
        ValueKind::Membership => "membership",
        ValueKind::Structured => "structured",
    }
}

pub fn structure_kind(value: StructureKind) -> &'static str {
    match value {
        StructureKind::None => "none",
        StructureKind::NetworkEnrichmentV1 => "network_enrichment_v1",
    }
}

pub fn meta_selection(value: MetaSelection) -> &'static str {
    match value {
        MetaSelection::ProvenCurrent => "proven_current",
        MetaSelection::SoleMeta0 => "sole_meta_0",
        MetaSelection::SoleMeta1 => "sole_meta_1",
    }
}

pub fn database_info(info: &DatabaseInfo) -> Value {
    json!({
        "address_family": address_family(info.address_family),
        "value_kind": value_kind(info.value_kind),
        "structure_kind": structure_kind(info.structure_kind),
        "value_tag": value_tag(info.value_tag.bytes()),
        "database_id": hex_id(&info.database_id),
        "transaction_id": decimal_u64(info.transaction_id),
        "commit_nonce": hex_id(&info.commit_nonce),
        "page_count": decimal_u64(info.page_count),
        "range_record_count": decimal_u64(info.range_record_count),
        "active_feed_count": decimal_u64(info.active_feed_count),
        "meta_selection": meta_selection(info.meta_selection),
    })
}

pub fn location(value: NetworkEnrichmentV1Location) -> Value {
    json!({
        "latitude_microdegrees": value.latitude_microdegrees,
        "longitude_microdegrees": value.longitude_microdegrees,
    })
}

pub fn network_enrichment(value: NetworkEnrichmentV1, threat_feeds: &[String]) -> Value {
    let location = value.location.map(location).unwrap_or_else(|| Value::Null);
    json!({
        "asn": value.asn,
        "country_id": value.country_id,
        "state_id": value.state_id,
        "city_id": value.city_id,
        "location": location,
        "threat_feeds": threat_feeds,
    })
}

pub fn cursor_address(point: CursorPoint) -> String {
    match point {
        CursorPoint::V4(value) => format!(
            "{}.{}.{}.{}",
            value >> 24,
            (value >> 16) & 0xff,
            (value >> 8) & 0xff,
            value & 0xff
        ),
        CursorPoint::V6(value) => std::net::Ipv6Addr::from(value.to_be_bytes()).to_string(),
    }
}

/// Convert a kind-1/kind-2 SDK local identity to its documented volume/file
/// decimal pair. Unsupported identity kinds remain handler errors.
#[cfg(test)]
pub fn file_identity(
    identity: &iprange_livedb::validation::LocalFileIdentity,
) -> Result<Value, ()> {
    let tail = &identity.bytes[16..];
    let device = u64::from_le_bytes(identity.bytes[0..8].try_into().map_err(|_| ())?);
    let file = u64::from_le_bytes(identity.bytes[8..16].try_into().map_err(|_| ())?);
    match identity.kind {
        1 if tail.iter().all(|byte| *byte == 0) => Ok(json!({
            "volume": decimal_u64(device),
            "file": decimal_u64(file),
        })),
        2 if identity.bytes[24..].iter().all(|byte| *byte == 0) => Ok(json!({
            "volume": decimal_u64(device),
            "file": decimal_u64(file),
        })),
        _ => Err(()),
    }
}

pub fn enrichment_view<'a>(view: &NetworkEnrichmentV1View<'a>, threat_feeds: &[String]) -> Value {
    network_enrichment(view.value(), threat_feeds)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exact_and_identity_conversions() {
        assert_eq!(decimal_u64(u64::MAX), "18446744073709551615");
        let mut identity = [0u8; 16];
        identity[..4].copy_from_slice(&[0x0a, 0xbc, 0xde, 0xf0]);
        assert_eq!(hex_id(&identity), format!("0abcdef0{}", "0".repeat(24)));
        assert_eq!(value_tag(b"direct"), json!({"hex": "646972656374"}));
    }

    #[test]
    fn kind_one_file_identity_decodes_little_endian_pair() {
        let mut bytes = [0u8; 32];
        bytes[..8].copy_from_slice(&0x0102030405060708u64.to_le_bytes());
        bytes[8..16].copy_from_slice(&0x090a0b0c0d0e0f10u64.to_le_bytes());
        let identity = iprange_livedb::validation::LocalFileIdentity { kind: 1, bytes };
        assert_eq!(
            file_identity(&identity).unwrap(),
            json!({"volume": "72623859790382856", "file": "651345242494996240"})
        );
    }

    #[test]
    fn unsupported_file_identity_kind_is_rejected() {
        let identity = iprange_livedb::validation::LocalFileIdentity {
            kind: 9,
            bytes: [1; 32],
        };
        assert_eq!(file_identity(&identity), Err(()));
    }

    #[test]
    fn cursor_addresses_are_canonical() {
        assert_eq!(cursor_address(CursorPoint::V4(0xc0000201)), "192.0.2.1");
        assert_eq!(
            cursor_address(CursorPoint::V6(0x20010db8000000000000000000000001)),
            "2001:db8::1"
        );
    }
}
