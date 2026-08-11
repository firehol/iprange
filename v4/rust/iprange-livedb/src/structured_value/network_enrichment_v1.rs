//! Typed semantics and canonical payload for `network_enrichment_v1`.

use crate::contract::{u32_le, StructureKind};
use crate::error::{Error, Result};
use crate::mapping::ByteSource;

use super::{Payload, PayloadCodec};

const ASN_OFFSET: usize = 0;
const COUNTRY_OFFSET: usize = 4;
const STATE_OFFSET: usize = 8;
const CITY_OFFSET: usize = 12;
const LATITUDE_OFFSET: usize = 16;
const LONGITUDE_OFFSET: usize = 20;
const MEMBERSHIP_OFFSET: usize = 24;
const FLAGS_OFFSET: usize = 28;
const PAYLOAD_SIZE: usize = 32;
const HAS_LOCATION: u32 = 1;
const LATITUDE_LIMIT: i32 = 90_000_000;
const LONGITUDE_LIMIT: i32 = 180_000_000;

/// Signed WGS84 coordinates in millionths of a degree.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct NetworkEnrichmentV1Location {
    pub latitude_microdegrees: i32,
    pub longitude_microdegrees: i32,
}

/// Fixed scalar portion of one network-enrichment result.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct NetworkEnrichmentV1 {
    pub asn: u32,
    pub country_id: u32,
    pub state_id: u32,
    pub city_id: u32,
    pub location: Option<NetworkEnrichmentV1Location>,
}

pub(crate) struct Codec;

impl Codec {
    pub(crate) fn encode(value: NetworkEnrichmentV1, membership_id: u32) -> Result<Payload> {
        validate_location(value.location)?;
        let mut bytes = [0u8; PAYLOAD_SIZE];
        bytes[ASN_OFFSET..COUNTRY_OFFSET].copy_from_slice(&value.asn.to_le_bytes());
        bytes[COUNTRY_OFFSET..STATE_OFFSET].copy_from_slice(&value.country_id.to_le_bytes());
        bytes[STATE_OFFSET..CITY_OFFSET].copy_from_slice(&value.state_id.to_le_bytes());
        bytes[CITY_OFFSET..LATITUDE_OFFSET].copy_from_slice(&value.city_id.to_le_bytes());
        if let Some(location) = value.location {
            bytes[LATITUDE_OFFSET..LONGITUDE_OFFSET]
                .copy_from_slice(&location.latitude_microdegrees.to_le_bytes());
            bytes[LONGITUDE_OFFSET..MEMBERSHIP_OFFSET]
                .copy_from_slice(&location.longitude_microdegrees.to_le_bytes());
            bytes[FLAGS_OFFSET..PAYLOAD_SIZE].copy_from_slice(&HAS_LOCATION.to_le_bytes());
        }
        bytes[MEMBERSHIP_OFFSET..FLAGS_OFFSET].copy_from_slice(&membership_id.to_le_bytes());
        Payload::new(&bytes)
    }

    pub(crate) fn decode(payload: &Payload) -> Result<NetworkEnrichmentV1> {
        let bytes = payload.as_slice();
        <Self as PayloadCodec>::validate(bytes)?;
        Ok(Self::decode_mapped(bytes).0)
    }

    pub(crate) fn decode_mapped<S: ByteSource>(bytes: S) -> (NetworkEnrichmentV1, u32) {
        crate::work::structure_decode(1);
        let flags = u32_le(bytes, FLAGS_OFFSET);
        (
            NetworkEnrichmentV1 {
                asn: u32_le(bytes, ASN_OFFSET),
                country_id: u32_le(bytes, COUNTRY_OFFSET),
                state_id: u32_le(bytes, STATE_OFFSET),
                city_id: u32_le(bytes, CITY_OFFSET),
                location: (flags & HAS_LOCATION != 0).then(|| NetworkEnrichmentV1Location {
                    latitude_microdegrees: i32_at(bytes, LATITUDE_OFFSET),
                    longitude_microdegrees: i32_at(bytes, LONGITUDE_OFFSET),
                }),
            },
            u32_le(bytes, MEMBERSHIP_OFFSET),
        )
    }

    pub(crate) fn with_membership(payload: &Payload, membership_id: u32) -> Result<Payload> {
        <Self as PayloadCodec>::validate(payload.as_slice())?;
        let mut bytes = [0u8; PAYLOAD_SIZE];
        bytes.copy_from_slice(payload.as_slice());
        bytes[MEMBERSHIP_OFFSET..FLAGS_OFFSET].copy_from_slice(&membership_id.to_le_bytes());
        Payload::new(&bytes)
    }
}

impl PayloadCodec for Codec {
    const KIND: StructureKind = StructureKind::NetworkEnrichmentV1;
    const PAYLOAD_SIZE: usize = PAYLOAD_SIZE;

    fn validate<S: ByteSource>(payload: S) -> Result<()> {
        if payload.len() != PAYLOAD_SIZE {
            return Err(Error::Corrupt(
                "network enrichment payload length is invalid",
            ));
        }
        let flags = u32_le(payload, FLAGS_OFFSET);
        if flags & !HAS_LOCATION != 0 {
            return Err(Error::Corrupt(
                "network enrichment payload flags are invalid",
            ));
        }
        let latitude = i32_at(payload, LATITUDE_OFFSET);
        let longitude = i32_at(payload, LONGITUDE_OFFSET);
        if flags == 0 {
            if latitude != 0 || longitude != 0 {
                return Err(Error::Corrupt(
                    "absent network location has nonzero coordinates",
                ));
            }
        } else if latitude.unsigned_abs() > LATITUDE_LIMIT as u32
            || longitude.unsigned_abs() > LONGITUDE_LIMIT as u32
        {
            return Err(Error::Corrupt(
                "network enrichment coordinates are outside their limits",
            ));
        }
        Ok(())
    }

    fn membership_id(payload: &Payload) -> u32 {
        u32_le(payload.as_slice(), MEMBERSHIP_OFFSET)
    }

    fn is_absent(payload: &Payload) -> bool {
        payload.as_slice().iter().all(|byte| *byte == 0)
    }
}

fn validate_location(location: Option<NetworkEnrichmentV1Location>) -> Result<()> {
    if location.is_some_and(|value| {
        value.latitude_microdegrees.unsigned_abs() > LATITUDE_LIMIT as u32
            || value.longitude_microdegrees.unsigned_abs() > LONGITUDE_LIMIT as u32
    }) {
        return Err(Error::InvalidArgument(
            "network enrichment coordinates are outside their limits",
        ));
    }
    Ok(())
}

fn i32_at<S: ByteSource>(source: S, offset: usize) -> i32 {
    u32_le(source, offset) as i32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codec_round_trips_present_zero_location() {
        let value = NetworkEnrichmentV1 {
            asn: 64512,
            country_id: 7,
            state_id: 8,
            city_id: 9,
            location: Some(NetworkEnrichmentV1Location {
                latitude_microdegrees: 0,
                longitude_microdegrees: 0,
            }),
        };
        let payload = Codec::encode(value, 42).unwrap();
        assert_eq!(Codec::decode(&payload).unwrap(), value);
        assert_eq!(Codec::membership_id(&payload), 42);
    }

    #[test]
    fn big_endian_portable_network_enrichment_v1_payload_matches_literal_bytes() {
        let payload = Codec::encode(
            NetworkEnrichmentV1 {
                asn: 0x1122_3344,
                country_id: 0x5566_7788,
                state_id: 0x99aa_bbcc,
                city_id: 0xddee_ff00,
                location: Some(NetworkEnrichmentV1Location {
                    latitude_microdegrees: 0x0102_0304,
                    longitude_microdegrees: -0x0102_0304,
                }),
            },
            0x0a0b_0c0d,
        )
        .unwrap();
        assert_eq!(
            payload.as_slice(),
            [
                0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55, 0xcc, 0xbb, 0xaa, 0x99, 0x00, 0xff,
                0xee, 0xdd, 0x04, 0x03, 0x02, 0x01, 0xfc, 0xfc, 0xfd, 0xfe, 0x0d, 0x0c, 0x0b, 0x0a,
                0x01, 0x00, 0x00, 0x00,
            ]
        );
    }

    #[test]
    fn codec_rejects_noncanonical_absent_location() {
        let mut bytes = [0; PAYLOAD_SIZE];
        bytes[LATITUDE_OFFSET] = 1;
        assert!(Codec::validate(&bytes[..]).is_err());
    }
}
