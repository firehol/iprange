//! Family trait: the IPv4/IPv6 behavioral differences of the legacy
//! surface as one authoritative interface (the C code duplicated the
//! whole pipeline per family; the Rust port keeps one generic core).

use crate::legacy::range::{IpNum, Range};

/// The address family of one legacy run. `-4` and the default are
/// identical; `-6` selects the IPv6 semantics (mapped-IPv4 input
/// normalization, /128 default prefix, u128 decimal counts).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Family {
    #[default]
    V4,
    V6,
}

/// Family-specific parsing and formatting hooks used by the generic
/// load/print pipeline. Implemented for `u32` (IPv4) and `u128`
/// (IPv6) in [`super::ipv4`] and [`super::ipv6`].
pub trait FamilyImpl: IpNum + 'static {
    /// The family this implementation serves.
    const FAMILY: Family;

    /// Parse one address (no prefix) from an input token. IPv4
    /// implements the inet_aton numeric forms (dotted, a.b.c, a.b,
    /// bare integer, octal, hex); IPv6 implements inet_pton-style
    /// parsing with IPv4-mapped normalization to `::ffff:x.x.x.x`.
    fn parse_addr(token: &str) -> Result<Self, String>;

    /// Parse `ADDR/PREFIX` and return the address range after the
    /// `cidr_use_network` policy (fix-network default, or
    /// dont-fix-network raw host start with prefix broadcast).
    fn parse_cidr(
        token: &str,
        prefix: u32,
        fix_network: bool,
        default_prefix: u32,
    ) -> Result<Range<Self>, String>;

    /// Parse a prefix length with family-exact strictness (reject
    /// empty, non-numeric, hex-like, oversized).
    fn parse_prefix(text: &str) -> Result<u32, String>;

    /// Canonical text form of one address (a.b.c.d; IPv6 via the
    /// equivalent of inet_ntop, so mapped v4 prints `::ffff:a.b.c.d`).
    fn fmt_addr(addr: Self) -> String;

    /// Canonical `addr/prefix` text (prefix 0..BITS-1); the full-width
    /// prefix prints as a bare address instead (C print_addr rule).
    fn fmt_cidr(addr: Self, prefix: u32) -> String;

    /// Input-family policy for tokens of the other family (v4 mode
    /// sees IPv6 text): None means drop with a counter (non-mapped
    /// IPv6 in v4 mode), Some(range) converts mapped IPv4 back.
    fn convert_foreign(token: &str) -> Option<Range<Self>>;

    /// The family default prefix for bare addresses (v4:
    /// `--default-prefix`; v6: always 128).
    fn default_prefix(options: &super::options::Options) -> u32 {
        match Self::FAMILY {
            Family::V4 => options.default_prefix,
            Family::V6 => 128,
        }
    }
}
