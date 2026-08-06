use super::*;

fn identity(device: u64, inode: u64) -> [u8; 32] {
    let mut bytes = [0; 32];
    bytes[..8].copy_from_slice(&device.to_le_bytes());
    bytes[8..16].copy_from_slice(&inode.to_le_bytes());
    bytes
}

fn header(policy: Policy) -> Header {
    Header {
        state: State::Prepared,
        database_id: [1; 16],
        transaction_id: 7,
        commit_nonce: [2; 16],
        attempt_id: [3; 16],
        reservation_identity: identity(4, 5),
        policy,
        output_byte_length: 3 * PAGE_SIZE as u64,
        output_identity: identity(4, 6),
        output_sha512: [7; 64],
        previous: policy.is_replacement().then_some(Previous {
            identity: identity(4, 8),
            byte_length: 0,
            sha512: [9; 64],
        }),
        basename_len: 7,
        basename_commitment: [10; 32],
        security_commitment: [11; 32],
        sequence: 1,
    }
}

fn one_block(header: Header) -> [u8; FILE_SIZE] {
    let mut bytes = [0; FILE_SIZE];
    encode_block(header, &mut bytes[..PAGE_SIZE]);
    bytes
}

fn encode_block(header: Header, block: &mut [u8]) {
    let page: &mut [u8; PAGE_SIZE] = block.try_into().expect("fixed reservation block");
    header.encode(page).unwrap();
}

fn rewrite_crc(block: &mut [u8]) {
    block[CRC_OFFSET..CRC_OFFSET + 4].fill(0);
    let checksum = crc32c::crc32c_with_zeroed(block, CRC_OFFSET, 4).unwrap();
    block[CRC_OFFSET..CRC_OFFSET + 4].copy_from_slice(&checksum.to_le_bytes());
}

#[test]
fn either_legitimate_surviving_block_is_authoritative() {
    let prepared = header(Policy::FailIfExists);
    let first = one_block(prepared);
    assert_eq!(
        select(&first).unwrap(),
        Selected {
            header: prepared,
            block: 0
        }
    );

    let attempted = prepared.state2().unwrap();
    let mut second = [0; FILE_SIZE];
    encode_block(attempted, &mut second[PAGE_SIZE..]);
    assert_eq!(
        select(&second).unwrap(),
        Selected {
            header: attempted,
            block: 1
        }
    );
}

#[test]
fn adjacent_state2_is_selected_and_a_torn_copy_falls_back() {
    let first = header(Policy::FailIfExists);
    let second = first.state2().unwrap();
    let mut bytes = one_block(first);
    encode_block(second, &mut bytes[PAGE_SIZE..]);
    assert_eq!(
        select(&bytes).unwrap(),
        Selected {
            header: second,
            block: 1
        }
    );

    bytes[PAGE_SIZE + 160] ^= 1;
    assert_eq!(
        select(&bytes).unwrap(),
        Selected {
            header: first,
            block: 0
        }
    );
}

#[test]
fn selection_rejects_disagreement_gaps_and_invalid_transitions() {
    let first = header(Policy::FailIfExists);
    let mut bytes = one_block(first);

    let mut different = first;
    different.output_sha512[0] ^= 1;
    encode_block(different, &mut bytes[PAGE_SIZE..]);
    assert_eq!(select(&bytes), Err(SelectError::EqualSequenceDisagreement));

    let mut gap = first;
    gap.sequence = 3;
    encode_block(gap, &mut bytes[PAGE_SIZE..]);
    assert_eq!(
        select(&bytes).unwrap(),
        Selected {
            header: first,
            block: 0
        },
        "an invalid newer copy must not hide the intact prepared copy"
    );

    let mut changed = first.state2().unwrap();
    changed.attempt_id[0] ^= 1;
    encode_block(changed, &mut bytes[PAGE_SIZE..]);
    assert_eq!(select(&bytes), Err(SelectError::AttemptMismatch));

    bytes.fill(0);
    encode_block(first.state2().unwrap(), &mut bytes[..PAGE_SIZE]);
    assert_eq!(select(&bytes), Err(SelectError::InvalidTransition));
}

#[test]
fn every_policy_has_exact_previous_fields() {
    let absent = one_block(header(Policy::FailIfExists));
    assert_eq!(
        select(&absent).unwrap().header.previous,
        None,
        "fail-if-exists must not invent prior bytes"
    );

    for policy in [Policy::ReplaceExisting, Policy::ReplaceExistingNoRollback] {
        let replacement = header(policy);
        let bytes = one_block(replacement);
        assert_eq!(
            select(&bytes).unwrap().header,
            replacement,
            "policy {policy:?}"
        );

        let mut invalid = bytes;
        invalid[116..120].fill(0);
        rewrite_crc(&mut invalid[..PAGE_SIZE]);
        assert!(matches!(
            select(&invalid),
            Err(SelectError::NoValidHeader {
                block0: Problem::Previous,
                ..
            })
        ));
    }
}

#[test]
fn reserved_bytes_unknown_kinds_and_malformed_output_fail_closed() {
    let original = one_block(header(Policy::FailIfExists));
    for (offset, problem) in [
        (600, Problem::Reserved),
        (112, Problem::Policy),
        (114, Problem::Fixed),
        (128 + 16, Problem::Output),
    ] {
        let mut bytes = original;
        bytes[offset] ^= 1;
        rewrite_crc(&mut bytes[..PAGE_SIZE]);
        assert!(
            matches!(
                select(&bytes),
                Err(SelectError::NoValidHeader { block0, .. }) if block0 == problem
            ),
            "offset {offset}"
        );
    }
}

#[test]
fn wrong_size_and_crc_corruption_are_distinct() {
    assert_eq!(select(&[0; 1]), Err(SelectError::WrongSize));

    let mut bytes = one_block(header(Policy::FailIfExists));
    bytes[20] ^= 1;
    assert!(matches!(
        select(&bytes),
        Err(SelectError::NoValidHeader {
            block0: Problem::Checksum,
            ..
        })
    ));
}

#[test]
fn empty_creation_security_commitment_is_rejected() {
    let mut bytes = one_block(header(Policy::FailIfExists));
    bytes[464..496].fill(0);
    rewrite_crc(&mut bytes[..PAGE_SIZE]);
    assert!(matches!(
        select(&bytes),
        Err(SelectError::NoValidHeader {
            block0: Problem::Security,
            ..
        })
    ));
}
