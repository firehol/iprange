use std::net::{Ipv4Addr, Ipv6Addr};

use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Corpus {
    pub(crate) schema: u32,
    pub(crate) fixtures: Vec<Fixture>,
    pub(crate) invalid_cases: Vec<InvalidCase>,
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub(crate) enum Family {
    Ipv4,
    Ipv6,
}

impl Family {
    pub(crate) fn parse(self, text: &str) -> u128 {
        match self {
            Self::Ipv4 => u128::from(u32::from(
                text.parse::<Ipv4Addr>()
                    .unwrap_or_else(|error| panic!("invalid IPv4 address {text}: {error}")),
            )),
            Self::Ipv6 => u128::from(
                text.parse::<Ipv6Addr>()
                    .unwrap_or_else(|error| panic!("invalid IPv6 address {text}: {error}")),
            ),
        }
    }

    pub(crate) const fn minimum(self) -> u128 {
        0
    }

    pub(crate) const fn maximum(self) -> u128 {
        match self {
            Self::Ipv4 => u32::MAX as u128,
            Self::Ipv6 => u128::MAX,
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub(crate) enum Kind {
    Direct,
    Membership,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Fixture {
    pub(crate) file: String,
    pub(crate) producer: String,
    pub(crate) family: Family,
    pub(crate) kind: Kind,
    pub(crate) tag: String,
    pub(crate) metadata: MetadataExpectation,
    pub(crate) address_count: String,
    #[serde(default)]
    pub(crate) direct_ranges: Vec<DirectExpectation>,
    #[serde(default)]
    pub(crate) feeds: Vec<FeedExpectation>,
    #[serde(default)]
    pub(crate) membership_ranges: Vec<MembershipExpectation>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct DirectExpectation {
    pub(crate) from: String,
    pub(crate) to: String,
    pub(crate) value: u32,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct FeedExpectation {
    pub(crate) name: String,
    pub(crate) index: u32,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct MembershipExpectation {
    pub(crate) from: String,
    pub(crate) to: String,
    pub(crate) feeds: Vec<String>,
}

#[derive(Debug, Deserialize)]
#[serde(tag = "state", rename_all = "lowercase", deny_unknown_fields)]
pub(crate) enum MetadataExpectation {
    Absent,
    Empty,
    Text { value: String },
    Repeat { byte: u8, length: usize },
}

impl MetadataExpectation {
    pub(crate) fn bytes(&self) -> Option<Vec<u8>> {
        match self {
            Self::Absent => None,
            Self::Empty => Some(Vec::new()),
            Self::Text { value } => Some(value.as_bytes().to_vec()),
            Self::Repeat { byte, length } => Some(vec![*byte; *length]),
        }
    }
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct InvalidCase {
    pub(crate) source: String,
    pub(crate) mutation: InvalidMutation,
    pub(crate) expected_error: ExpectedError,
}

#[derive(Clone, Copy, Debug, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum InvalidMutation {
    WrongMagic,
    Short,
    Unaligned,
}

#[derive(Clone, Copy, Debug, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum ExpectedError {
    FormatInvalid,
}
