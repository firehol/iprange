use crate::slotted_page::put_u32;

use super::*;

impl Header {
    fn file_bytes(&self) -> [u8; FILE_SIZE] {
        let mut bytes = [0; FILE_SIZE];
        let (left, right) = bytes.split_at_mut(PAGE_SIZE);
        let left: &mut [u8; PAGE_SIZE] = left.try_into().expect("fixed GC block");
        let right: &mut [u8; PAGE_SIZE] = right.try_into().expect("fixed GC block");
        self.encode(left).unwrap();
        self.encode(right).unwrap();
        bytes
    }
}

fn header() -> Header {
    let source_basename = b"s\0o\0u\0r\0c\0e\0".to_vec().into_boxed_slice();
    Header {
        kind: ArtifactKind::PrivateOutput,
        basename_encoding: 2,
        attempt_id: [1; 16],
        ordinal: 7,
        directory_identity_kind: 2,
        artifact_identity_kind: 2,
        directory_identity: [2; 32],
        source_commitment: source_commitment(2, &source_basename),
        inert_commitment: inert_commitment(2, b"inert"),
        artifact_identity: [3; 32],
        payload: Some(Payload {
            byte_length: 8192,
            sha512: [4; 64],
            database_id: [5; 16],
            transaction_id: 6,
            commit_nonce: [7; 16],
        }),
        creation_security_kind: 2,
        directory_role: DirectoryRole::Destination,
        creation_security_commitment: [8; 32],
        source_basename,
        sequence: 1,
    }
}

#[test]
fn exact_layout_round_trips_with_either_complete_copy() {
    let expected = header();
    let mut bytes = expected.file_bytes();
    assert_eq!(select(&bytes), Ok(expected.clone()));
    assert_eq!(&bytes[..8], b"IPR4GCA1");
    assert_eq!(u16_le(&bytes, 8), 512);
    assert_eq!(u16_le(&bytes, 136), 1);
    assert_eq!(u16_le(&bytes, 290), 1);
    assert_eq!(u64_le(&bytes, 496), 1);
    bytes[508] ^= 1;
    assert_eq!(select(&bytes), Ok(expected));
}

#[test]
fn malformed_reserved_payload_and_disagreement_fail_closed() {
    let expected = header();
    let mut bytes = expected.file_bytes();
    bytes[292] = 1;
    bytes[PAGE_SIZE + 292] = 1;
    assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));

    let mut bytes = expected.file_bytes();
    let other = Header {
        ordinal: 8,
        sequence: 2,
        ..expected.clone()
    }
    .file_bytes();
    bytes[PAGE_SIZE..].copy_from_slice(&other[..PAGE_SIZE]);
    assert_eq!(select(&bytes), Err(SelectError::HeaderDisagreement));
}

#[test]
fn unknown_payload_requires_every_optional_field_zero() {
    let expected = Header {
        payload: None,
        ..header()
    };
    let mut bytes = expected.file_bytes();
    assert_eq!(select(&bytes), Ok(expected.clone()));
    bytes[176] = 1;
    let checksum = crc32c::crc32c_with_zeroed(&bytes[..PAGE_SIZE], CRC_OFFSET, 4).unwrap();
    put_u32(&mut bytes, CRC_OFFSET, checksum);
    let (left, right) = bytes.split_at_mut(PAGE_SIZE);
    right.copy_from_slice(left);
    assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));
}

#[test]
fn source_filename_is_stored_without_a_path_and_authenticated() {
    let expected = header();
    let mut bytes = expected.file_bytes();
    assert_eq!(
        u32_le(&bytes, SOURCE_LENGTH_OFFSET) as usize,
        expected.source_basename.len()
    );
    assert_eq!(
        &bytes[SOURCE_OFFSET..SOURCE_OFFSET + expected.source_basename.len()],
        expected.source_basename.as_ref()
    );

    bytes[SOURCE_OFFSET] ^= 1;
    let checksum = crc32c::crc32c_with_zeroed(&bytes[..PAGE_SIZE], CRC_OFFSET, 4).unwrap();
    put_u32(&mut bytes, CRC_OFFSET, checksum);
    let (left, right) = bytes.split_at_mut(PAGE_SIZE);
    right.copy_from_slice(left);
    assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));
}

#[test]
fn exact_payload_may_describe_arbitrary_non_v4_bytes() {
    let expected = Header {
        payload: Some(Payload {
            byte_length: 7,
            sha512: [9; 64],
            database_id: [0; 16],
            transaction_id: 0,
            commit_nonce: [0; 16],
        }),
        ..header()
    };
    assert_eq!(select(&expected.file_bytes()), Ok(expected));
}
