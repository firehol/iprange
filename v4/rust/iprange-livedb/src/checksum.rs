//! Hardware-dispatched CRC-32C checksums used by v4 pages.

const ZEROES: [u8; 64] = [0; 64];

pub(crate) fn crc32c_with_zeroed(bytes: &[u8], zero_at: usize, zero_len: usize) -> Option<u32> {
    let zero_end = zero_at.checked_add(zero_len)?;
    if zero_end > bytes.len() {
        return None;
    }

    let mut crc = ::crc32c::crc32c(&bytes[..zero_at]);
    let mut remaining = zero_len;
    while remaining >= ZEROES.len() {
        crc = ::crc32c::crc32c_append(crc, &ZEROES);
        remaining -= ZEROES.len();
    }
    if remaining != 0 {
        crc = ::crc32c::crc32c_append(crc, &ZEROES[..remaining]);
    }
    Some(::crc32c::crc32c_append(crc, &bytes[zero_end..]))
}

#[cfg(test)]
mod tests {
    use super::*;

    const POLYNOMIAL: u32 = 0x82f6_3b78;

    fn reference(bytes: &[u8]) -> u32 {
        let mut crc = u32::MAX;
        for &byte in bytes {
            crc ^= u32::from(byte);
            for _ in 0..8 {
                let mask = 0u32.wrapping_sub(crc & 1);
                crc = (crc >> 1) ^ (POLYNOMIAL & mask);
            }
        }
        !crc
    }

    #[test]
    fn standard_vectors_match() {
        assert_eq!(::crc32c::crc32c(b""), 0);
        assert_eq!(::crc32c::crc32c(b"123456789"), 0xe306_9283);
    }

    #[test]
    fn zeroed_range_matches_a_copy() {
        let original = b"abcdefgh";
        let mut copy = *original;
        copy[2..6].fill(0);
        assert_eq!(crc32c_with_zeroed(original, 2, 4), Some(reference(&copy)));
        assert_eq!(crc32c_with_zeroed(original, 7, 2), None);
    }

    #[test]
    fn dispatched_backend_matches_the_independent_reference() {
        let mut bytes = [0u8; 4096];
        for (index, byte) in bytes.iter_mut().enumerate() {
            *byte = (index % 251) as u8;
        }
        for len in [0, 1, 7, 8, 31, 32, 255, 256, 4095, 4096] {
            assert_eq!(::crc32c::crc32c(&bytes[..len]), reference(&bytes[..len]));
        }

        for zero_len in [0, 1, 4, 63, 64, 65, 129] {
            for zero_at in [0, 17, 2048, bytes.len() - zero_len] {
                let mut expected = bytes;
                expected[zero_at..zero_at + zero_len].fill(0);
                assert_eq!(
                    crc32c_with_zeroed(&bytes, zero_at, zero_len),
                    Some(reference(&expected))
                );
            }
        }
    }
}
