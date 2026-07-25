//! Exact platform basename encoding and commitment.

use sha2::{Digest, Sha256};

const NAME_DOMAIN: &[u8; 8] = b"IPR4NAME";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum BasenameEncoding {
    #[cfg(any(test, unix))]
    PosixBytes = 1,
    #[cfg(any(test, target_os = "windows"))]
    WindowsUtf16Le = 2,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum BasenameBindingError {
    Empty,
    TooLong,
    #[cfg(any(test, unix))]
    InvalidPosixComponent,
    #[cfg(any(test, target_os = "windows"))]
    InvalidWindowsComponent,
    #[cfg(any(test, target_os = "windows"))]
    InvalidUtf16,
}

pub(crate) fn basename_commitment(
    encoding: BasenameEncoding,
    bytes: &[u8],
) -> Result<[u8; 32], BasenameBindingError> {
    if bytes.is_empty() {
        return Err(BasenameBindingError::Empty);
    }
    let length = u32::try_from(bytes.len()).map_err(|_| BasenameBindingError::TooLong)?;
    match encoding {
        #[cfg(any(test, unix))]
        BasenameEncoding::PosixBytes => validate_posix(bytes)?,
        #[cfg(any(test, target_os = "windows"))]
        BasenameEncoding::WindowsUtf16Le => validate_windows_utf16le(bytes)?,
    }

    let mut hasher = Sha256::new();
    hasher.update(NAME_DOMAIN);
    hasher.update((encoding as u16).to_le_bytes());
    hasher.update(length.to_le_bytes());
    hasher.update(bytes);
    Ok(hasher.finalize().into())
}

#[cfg(any(test, unix))]
fn validate_posix(bytes: &[u8]) -> Result<(), BasenameBindingError> {
    if bytes == b"." || bytes == b".." || bytes.contains(&0) || bytes.contains(&b'/') {
        return Err(BasenameBindingError::InvalidPosixComponent);
    }
    Ok(())
}

#[cfg(any(test, target_os = "windows"))]
fn validate_windows_utf16le(bytes: &[u8]) -> Result<(), BasenameBindingError> {
    if bytes.len() % 2 != 0 {
        return Err(BasenameBindingError::InvalidUtf16);
    }
    let mut units = bytes
        .chunks_exact(2)
        .map(|pair| u16::from_le_bytes([pair[0], pair[1]]));
    while let Some(unit) = units.next() {
        if unit == 0 || unit == b'/' as u16 || unit == b'\\' as u16 {
            return Err(BasenameBindingError::InvalidWindowsComponent);
        }
        if (0xd800..=0xdbff).contains(&unit) {
            let Some(low) = units.next() else {
                return Err(BasenameBindingError::InvalidUtf16);
            };
            if !(0xdc00..=0xdfff).contains(&low) {
                return Err(BasenameBindingError::InvalidUtf16);
            }
        } else if (0xdc00..=0xdfff).contains(&unit) {
            return Err(BasenameBindingError::InvalidUtf16);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn posix_commitment_matches_the_normative_byte_formula() {
        assert_eq!(
            basename_commitment(BasenameEncoding::PosixBytes, b"main.iprdb"),
            Ok([
                0x58, 0x1c, 0x42, 0x34, 0xbf, 0xf2, 0x93, 0x4f, 0xab, 0x8a, 0x83, 0x4b, 0x0c, 0x4b,
                0x38, 0x98, 0xac, 0xc6, 0xe6, 0xe0, 0x01, 0x92, 0x7a, 0xe1, 0xc0, 0x9d, 0x09, 0xb6,
                0xf4, 0xa8, 0x3c, 0x20,
            ])
        );
        assert_eq!(
            basename_commitment(BasenameEncoding::PosixBytes, b"."),
            Err(BasenameBindingError::InvalidPosixComponent)
        );
        assert_eq!(
            basename_commitment(BasenameEncoding::PosixBytes, b"a/b"),
            Err(BasenameBindingError::InvalidPosixComponent)
        );
    }

    #[test]
    fn windows_encoding_is_well_formed_utf16le_and_one_component() {
        let valid = [b'm', 0, 0x3d, 0xd8, 0x00, 0xde];
        assert!(basename_commitment(BasenameEncoding::WindowsUtf16Le, &valid).is_ok());
        for invalid in [
            &[b'x'][..],
            &[0x00, 0xd8][..],
            &[0x00, 0xdc][..],
            &[b'/', 0][..],
            &[0, 0][..],
        ] {
            assert!(basename_commitment(BasenameEncoding::WindowsUtf16Le, invalid).is_err());
        }
    }
}
