//! Fixed-memory SHA-512 over exact publication bytes.

use sha2::{Digest, Sha512};

use crate::cancellation::CancellationToken;
use crate::mapping::Mapping;

use super::Error;

pub(super) const DIGEST_BUFFER_SIZE: usize = 1024;

pub(crate) fn digest(mapping: &Mapping, byte_length: u64) -> Result<[u8; 64], Error> {
    digest_with(byte_length, |offset, output| {
        if mapping.bytes(offset, output.len())?.copy_to(output) {
            Ok(())
        } else {
            Err(Error::FinishedLengthChanged)
        }
    })
}

pub(crate) fn digest_cancellable(
    mapping: &Mapping,
    byte_length: u64,
    cancellation: &CancellationToken,
) -> Result<[u8; 64], Error> {
    let result = digest_with(byte_length, |offset, output| {
        cancellation.check().map_err(Error::Sdk)?;
        if mapping.bytes(offset, output.len())?.copy_to(output) {
            Ok(())
        } else {
            Err(Error::FinishedLengthChanged)
        }
    });
    cancellation.check().map_err(Error::Sdk)?;
    result
}

pub(super) fn digest_with(
    byte_length: u64,
    mut read: impl FnMut(u64, &mut [u8]) -> Result<(), Error>,
) -> Result<[u8; 64], Error> {
    let mut hasher = Sha512::new();
    let mut buffer = [0; DIGEST_BUFFER_SIZE];
    let mut offset = 0;
    while offset < byte_length {
        let remaining = byte_length - offset;
        let length = usize::try_from(remaining.min(DIGEST_BUFFER_SIZE as u64))
            .expect("digest chunk fits usize");
        read(offset, &mut buffer[..length])?;
        hasher.update(&buffer[..length]);
        offset = offset
            .checked_add(length as u64)
            .ok_or(Error::FinishedLengthChanged)?;
    }
    Ok(hasher.finalize().into())
}
