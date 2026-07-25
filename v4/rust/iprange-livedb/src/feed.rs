//! Public named-feed values.

use std::fmt;

use crate::error::{Error, Result};

pub(crate) const MAX_FEED_NAME: usize = 255;

/// One validated structural feed name.
#[derive(Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct FeedName {
    bytes: [u8; MAX_FEED_NAME],
    len: u8,
}

impl FeedName {
    /// Validate and copy one feed name.
    pub fn new(name: &str) -> Result<Self> {
        Self::from_stored(name.as_bytes()).ok_or(Error::NameInvalid)
    }

    /// The exact lowercase ASCII name.
    pub fn as_str(&self) -> &str {
        std::str::from_utf8(self.as_bytes()).expect("validated feed names are ASCII")
    }

    /// The exact name bytes.
    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes[..usize::from(self.len)]
    }

    pub(crate) fn from_stored(name: &[u8]) -> Option<Self> {
        if !valid(name) {
            return None;
        }
        let mut bytes = [0; MAX_FEED_NAME];
        bytes[..name.len()].copy_from_slice(name);
        Some(Self {
            bytes,
            len: name.len() as u8,
        })
    }
}

impl AsRef<str> for FeedName {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}

impl fmt::Display for FeedName {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.write_str(self.as_str())
    }
}

impl fmt::Debug for FeedName {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_tuple("FeedName")
            .field(&self.as_str())
            .finish()
    }
}

/// One copied feed-catalog entry from a pinned generation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FeedEntry {
    pub name: FeedName,
    pub index: u32,
}

fn valid(name: &[u8]) -> bool {
    let Some((&first, rest)) = name.split_first() else {
        return false;
    };
    if name.len() > MAX_FEED_NAME || !alphanumeric(first) {
        return false;
    }
    let Some((&last, middle)) = rest.split_last() else {
        return true;
    };
    alphanumeric(last) && middle.iter().copied().all(allowed)
}

fn alphanumeric(byte: u8) -> bool {
    byte.is_ascii_lowercase() || byte.is_ascii_digit()
}

fn allowed(byte: u8) -> bool {
    alphanumeric(byte) || matches!(byte, b'_' | b'-' | b'.')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exact_name_grammar_and_boundaries() {
        for name in ["a", "0", "a_b-c.d9"] {
            assert_eq!(FeedName::new(name).unwrap().as_str(), name);
        }
        let maximum = format!("a{}z", "_".repeat(253));
        assert_eq!(FeedName::new(&maximum).unwrap().as_str(), maximum);

        for name in [
            "", "_a", "a_", "-a", "a-", ".a", "a.", "A", "a/b", "a b", "é",
        ] {
            assert!(matches!(FeedName::new(name), Err(Error::NameInvalid)));
        }
        assert!(matches!(
            FeedName::new(&format!("a{}", "b".repeat(255))),
            Err(Error::NameInvalid)
        ));
    }
}
