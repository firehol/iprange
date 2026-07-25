//! Nonzero identities from the operating-system CSPRNG.

use crate::error::{Error, Result};

pub(crate) fn nonzero_128() -> Result<[u8; 16]> {
    let mut value = [0; 16];
    getrandom::fill(&mut value)?;
    if value == [0; 16] {
        return Err(Error::Corrupt(
            "operating-system randomness returned an all-zero identity",
        ));
    }
    Ok(value)
}
