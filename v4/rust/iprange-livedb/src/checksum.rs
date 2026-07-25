//! CRC-32C checksums used by v4 pages.

const POLYNOMIAL: u32 = 0x82f6_3b78;
const TABLE: [u32; 256] = build_table();

const fn build_table() -> [u32; 256] {
    let mut table = [0; 256];
    let mut index = 0;
    while index < table.len() {
        let mut crc = index as u32;
        let mut bit = 0;
        while bit < 8 {
            let mask = 0u32.wrapping_sub(crc & 1);
            crc = (crc >> 1) ^ (POLYNOMIAL & mask);
            bit += 1;
        }
        table[index] = crc;
        index += 1;
    }
    table
}

fn update(mut crc: u32, bytes: &[u8]) -> u32 {
    for &byte in bytes {
        let index = ((crc ^ u32::from(byte)) & 0xff) as usize;
        crc = (crc >> 8) ^ TABLE[index];
    }
    crc
}

pub(crate) fn crc32c_with_zeroed(bytes: &[u8], zero_at: usize, zero_len: usize) -> Option<u32> {
    let zero_end = zero_at.checked_add(zero_len)?;
    if zero_end > bytes.len() {
        return None;
    }

    let mut crc = update(u32::MAX, &bytes[..zero_at]);
    for _ in 0..zero_len {
        crc = update(crc, &[0]);
    }
    Some(!update(crc, &bytes[zero_end..]))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn crc32c(bytes: &[u8]) -> u32 {
        !update(u32::MAX, bytes)
    }

    #[test]
    fn standard_vectors_match() {
        assert_eq!(crc32c(b""), 0);
        assert_eq!(crc32c(b"123456789"), 0xe306_9283);
    }

    #[test]
    fn zeroed_range_matches_a_copy() {
        let original = b"abcdefgh";
        let mut copy = *original;
        copy[2..6].fill(0);
        assert_eq!(crc32c_with_zeroed(original, 2, 4), Some(crc32c(&copy)));
        assert_eq!(crc32c_with_zeroed(original, 7, 2), None);
    }
}
