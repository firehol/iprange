//! IP keys for the index: IPv4 = one `u32`, IPv6 = two `u64` (`hi` then `lo`),
//! compared **numerically** (§4). The hot path uses only `u64` comparisons — no native
//! 128-bit type — so the same algorithm compiles for Go. The `[from, to, scope]`
//! record size is `scope_width`-dependent (§4), so — unlike the v3 key — the width is
//! kept here but the record size is not (it lives in [`crate::spec::record_size`]).
//!
//! [`IpKey::checked_inc`] / [`IpKey::checked_dec`] implement the §4 `u128_inc` /
//! `u128_dec` boundary rules used by `set` / `delete` to trim at `from − 1` / `to + 1`.

use crate::spec::IpVersion;

/// Common interface over the two key widths, so the index/reader/writer algorithms are
/// written once and width-specialized at compile time (monomorphization), never
/// widening IPv4 to 128-bit (§4).
pub trait IpKey: Copy + Ord + core::fmt::Debug + 'static {
    /// Key width in bytes (4 or 16).
    const WIDTH: usize;
    /// The IP family.
    const VERSION: IpVersion;
    /// The minimum address (`0.0.0.0` / `::`), i.e. `family_min` (§4).
    const MIN: Self;
    /// The maximum address (all-ones), i.e. `family_max` (§4).
    const MAX: Self;

    /// Serialize the key little-endian into the first [`WIDTH`](Self::WIDTH) bytes of
    /// `out`. Panics if `out` is shorter than `WIDTH`.
    fn write_le(self, out: &mut [u8]);

    /// Deserialize a key from the first [`WIDTH`](Self::WIDTH) bytes of `src`. Panics
    /// if `src` is shorter than `WIDTH`.
    fn read_le(src: &[u8]) -> Self;

    /// `self + 1`, or `None` if `self` is `family_max` (no `+1` exists, §4). Used to
    /// right-trim at `to + 1` after the family-boundary pre-check.
    fn checked_inc(self) -> Option<Self>;

    /// `self - 1`, or `None` if `self` is `family_min` (§4). Used to left-trim at
    /// `from − 1`.
    fn checked_dec(self) -> Option<Self>;
}

/// An IPv4 address as a big-endian-valued `u32` (e.g. `192.0.2.1` = `0xC000_0201`),
/// stored little-endian on disk. Derived `Ord` is numeric.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct Ipv4Key(pub u32);

impl IpKey for Ipv4Key {
    const WIDTH: usize = 4;
    const VERSION: IpVersion = IpVersion::V4;
    const MIN: Self = Ipv4Key(0);
    const MAX: Self = Ipv4Key(u32::MAX);

    #[inline]
    fn write_le(self, out: &mut [u8]) {
        out[..4].copy_from_slice(&self.0.to_le_bytes());
    }

    #[inline]
    fn read_le(src: &[u8]) -> Self {
        Ipv4Key(u32::from_le_bytes([src[0], src[1], src[2], src[3]]))
    }

    #[inline]
    fn checked_inc(self) -> Option<Self> {
        self.0.checked_add(1).map(Ipv4Key)
    }

    #[inline]
    fn checked_dec(self) -> Option<Self> {
        self.0.checked_sub(1).map(Ipv4Key)
    }
}

/// An IPv6 address as a `(hi, lo)` pair of `u64` — `hi` is the most-significant 64
/// bits. Stored on disk as `hi` little-endian then `lo` little-endian (§4). Field
/// order `(hi, lo)` makes the **derived** `Ord` exactly the numeric 128-bit order.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct Ipv6Key {
    /// Most-significant 64 bits.
    pub hi: u64,
    /// Least-significant 64 bits.
    pub lo: u64,
}

impl Ipv6Key {
    /// Construct from the full 128-bit value.
    #[inline]
    pub const fn from_u128(v: u128) -> Self {
        Ipv6Key {
            hi: (v >> 64) as u64,
            lo: v as u64,
        }
    }

    /// The full 128-bit value.
    #[inline]
    pub const fn to_u128(self) -> u128 {
        ((self.hi as u128) << 64) | (self.lo as u128)
    }
}

impl IpKey for Ipv6Key {
    const WIDTH: usize = 16;
    const VERSION: IpVersion = IpVersion::V6;
    const MIN: Self = Ipv6Key { hi: 0, lo: 0 };
    const MAX: Self = Ipv6Key {
        hi: u64::MAX,
        lo: u64::MAX,
    };

    #[inline]
    fn write_le(self, out: &mut [u8]) {
        out[..8].copy_from_slice(&self.hi.to_le_bytes());
        out[8..16].copy_from_slice(&self.lo.to_le_bytes());
    }

    #[inline]
    fn read_le(src: &[u8]) -> Self {
        let mut h = [0u8; 8];
        let mut l = [0u8; 8];
        h.copy_from_slice(&src[0..8]);
        l.copy_from_slice(&src[8..16]);
        Ipv6Key {
            hi: u64::from_le_bytes(h),
            lo: u64::from_le_bytes(l),
        }
    }

    #[inline]
    fn checked_inc(self) -> Option<Self> {
        // `u128_inc` (§4): lo' = lo + 1; hi' = hi + carry; None at all-ones.
        if self == Self::MAX {
            return None;
        }
        Some(if self.lo == u64::MAX {
            Ipv6Key {
                hi: self.hi + 1,
                lo: 0,
            }
        } else {
            Ipv6Key {
                hi: self.hi,
                lo: self.lo + 1,
            }
        })
    }

    #[inline]
    fn checked_dec(self) -> Option<Self> {
        // `u128_dec` (§4): borrow from hi when lo underflows; None at the minimum.
        if self == Self::MIN {
            return None;
        }
        Some(if self.lo == 0 {
            Ipv6Key {
                hi: self.hi - 1,
                lo: u64::MAX,
            }
        } else {
            Ipv6Key {
                hi: self.hi,
                lo: self.lo - 1,
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ipv6_worked_example_2001_db8_1() {
        // 2001:db8::1 -> hi=0x2001_0db8_0000_0000, lo=1 (key encoding shared with v3 §3).
        let k = Ipv6Key {
            hi: 0x2001_0db8_0000_0000,
            lo: 0x1,
        };
        let mut buf = [0u8; 16];
        k.write_le(&mut buf);
        let expected: [u8; 16] = [
            0x00, 0x00, 0x00, 0x00, 0xb8, 0x0d, 0x01, 0x20, // hi, little-endian
            0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // lo, little-endian
        ];
        assert_eq!(buf, expected, "IPv6 key on-disk bytes must match the encoding");
        assert_eq!(Ipv6Key::read_le(&buf), k, "round-trip");
        assert_eq!(k.to_u128(), 0x2001_0db8_0000_0000_0000_0000_0000_0001);
    }

    #[test]
    fn ipv4_worked_example_192_0_2_1() {
        // 192.0.2.1 = 0xC000_0201 -> LE bytes 01 02 00 c0.
        let k = Ipv4Key(0xC000_0201);
        let mut buf = [0u8; 4];
        k.write_le(&mut buf);
        assert_eq!(buf, [0x01, 0x02, 0x00, 0xc0]);
        assert_eq!(Ipv4Key::read_le(&buf), k);
    }

    #[test]
    fn ipv6_numeric_order_not_bytewise() {
        let a = Ipv6Key { hi: 1, lo: 0 };
        let b = Ipv6Key {
            hi: 0,
            lo: u64::MAX,
        };
        assert!(a > b, "compare hi then lo, not raw bytes");
        assert!(a.to_u128() > b.to_u128());
    }

    #[test]
    fn checked_inc_v6_carry_and_max() {
        assert_eq!(
            Ipv6Key {
                hi: 5,
                lo: u64::MAX
            }
            .checked_inc(),
            Some(Ipv6Key { hi: 6, lo: 0 }),
            "carry from lo into hi"
        );
        assert_eq!(
            Ipv6Key { hi: 0, lo: 41 }.checked_inc(),
            Some(Ipv6Key { hi: 0, lo: 42 })
        );
        assert_eq!(Ipv6Key::MAX.checked_inc(), None, "no +1 at family_max");
    }

    #[test]
    fn checked_dec_v6_borrow_and_min() {
        assert_eq!(
            Ipv6Key { hi: 6, lo: 0 }.checked_dec(),
            Some(Ipv6Key {
                hi: 5,
                lo: u64::MAX
            }),
            "borrow from hi into lo"
        );
        assert_eq!(Ipv6Key::MIN.checked_dec(), None, "no -1 at family_min");
    }

    #[test]
    fn checked_inc_dec_v4_bounds() {
        assert_eq!(Ipv4Key(41).checked_inc(), Some(Ipv4Key(42)));
        assert_eq!(Ipv4Key::MAX.checked_inc(), None);
        assert_eq!(Ipv4Key(42).checked_dec(), Some(Ipv4Key(41)));
        assert_eq!(Ipv4Key::MIN.checked_dec(), None);
    }
}
