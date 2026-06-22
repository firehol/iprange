//! IP keys for the index: IPv4 = one `u32`, IPv6 = two `u64` (`hi` then `lo`),
//! compared **numerically** (§3). The lookup hot path uses only `u64` comparisons —
//! no native 128-bit type — so the same algorithm compiles for Go. The producer-side
//! 128-bit helpers ([`IpKey::checked_inc`], [`IpKey::range_size`]) are the only place
//! 128-bit arithmetic is needed, and a reader needs `checked_inc` only for the
//! optional coalescing-invariant check.

use crate::spec::IpVersion;

/// Common interface over the two key widths, so the index/writer/reader algorithms
/// are written once and width-specialized at compile time (monomorphization), never
/// widening IPv4 to 128-bit (§3).
pub trait IpKey: Copy + Ord + core::fmt::Debug + 'static {
    /// Key width in bytes (4 or 16).
    const WIDTH: usize;
    /// Record size in bytes for this width (12 or 40).
    const RECORD_SIZE: usize;
    /// The IP family.
    const VERSION: IpVersion;
    /// The minimum address (`0.0.0.0` / `::`).
    const MIN: Self;
    /// The maximum address (all-ones).
    const MAX: Self;

    /// Serialize the key little-endian into the first [`WIDTH`](Self::WIDTH) bytes of
    /// `out`. Panics if `out` is shorter than `WIDTH`.
    fn write_le(self, out: &mut [u8]);

    /// Deserialize a key from the first [`WIDTH`](Self::WIDTH) bytes of `src`.
    /// Panics if `src` is shorter than `WIDTH`.
    fn read_le(src: &[u8]) -> Self;

    /// `self + 1`, or `None` if `self` is the family maximum (no `+1` exists, §9).
    /// Producer uses it for coalescing; a reader uses it only for the optional
    /// coalescing-invariant check, and MUST pre-check the maximum (this does).
    fn checked_inc(self) -> Option<Self>;

    /// `self - 1`, or `None` if `self` is the family minimum (`0.0.0.0` / `::`).
    /// The v3.1 merge sweep uses it to close an elementary interval at the address
    /// just before the next boundary (§13.3).
    fn checked_dec(self) -> Option<Self>;

    /// The number of addresses in the inclusive range `[start, self]` as `u128`
    /// (`self − start + 1`), or `None` if the count is unrepresentable in `u128`
    /// (only the full IPv6 space `[::, ffff:…:ffff]`, whose size is `2^128`, §5).
    /// `start` MUST be `<= self`.
    fn range_size(start: Self, end: Self) -> Option<u128>;
}

/// An IPv4 address as a big-endian-valued `u32` (e.g. `192.0.2.1` = `0xC000_0201`),
/// stored little-endian on disk. Derived `Ord` is numeric.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct Ipv4Key(pub u32);

impl IpKey for Ipv4Key {
    const WIDTH: usize = 4;
    const RECORD_SIZE: usize = 12;
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

    #[inline]
    fn range_size(start: Self, end: Self) -> Option<u128> {
        // Max IPv4 count is 2^32 — fits comfortably in u128 (and u64). Never None.
        debug_assert!(start <= end);
        Some(u128::from(end.0 - start.0) + 1)
    }
}

/// An IPv6 address as a `(hi, lo)` pair of `u64` — `hi` is the most-significant 64
/// bits. Stored on disk as `hi` little-endian then `lo` little-endian (§3). Field
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
    const RECORD_SIZE: usize = 40;
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
        // `u128_inc` semantics (§3): lo' = lo + 1; hi' = hi + carry; None at all-ones.
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
        // `u128_dec` (§3): borrow from hi when lo underflows; None at the minimum.
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

    #[inline]
    fn range_size(start: Self, end: Self) -> Option<u128> {
        debug_assert!(start <= end);
        // size = end - start + 1, checked. The only unrepresentable case is the full
        // space [0, 2^128-1] whose size is 2^128 (§5): detect it structurally first.
        if start == Self::MIN && end == Self::MAX {
            return None;
        }
        // end - start fits in u128; +1 cannot overflow because end < MAX here.
        Some((end.to_u128() - start.to_u128()) + 1)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ipv6_worked_example_2001_db8_1() {
        // Spec §3: 2001:db8::1 -> hi=0x2001_0db8_0000_0000, lo=1.
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
        assert_eq!(
            buf, expected,
            "IPv6 key on-disk bytes must match the spec example"
        );
        assert_eq!(Ipv6Key::read_le(&buf), k, "round-trip");
        assert_eq!(k.to_u128(), 0x2001_0db8_0000_0000_0000_0000_0000_0001);
    }

    #[test]
    fn ipv4_worked_example_192_0_2_1() {
        // Spec §3: 192.0.2.1 = 0xC000_0201 -> LE bytes 01 02 00 c0.
        let k = Ipv4Key(0xC000_0201);
        let mut buf = [0u8; 4];
        k.write_le(&mut buf);
        assert_eq!(buf, [0x01, 0x02, 0x00, 0xc0]);
        assert_eq!(Ipv4Key::read_le(&buf), k);
    }

    #[test]
    fn ipv6_numeric_order_not_bytewise() {
        // hi dominates: a smaller lo with a larger hi is greater.
        let a = Ipv6Key { hi: 1, lo: 0 };
        let b = Ipv6Key {
            hi: 0,
            lo: u64::MAX,
        };
        assert!(a > b, "compare hi then lo, not raw bytes");
        assert_eq!(a.to_u128(), 1u128 << 64);
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
        assert_eq!(
            Ipv6Key::MAX.checked_inc(),
            None,
            "no +1 at the family maximum"
        );
    }

    #[test]
    fn checked_inc_v4_max() {
        assert_eq!(Ipv4Key(41).checked_inc(), Some(Ipv4Key(42)));
        assert_eq!(Ipv4Key::MAX.checked_inc(), None);
    }

    #[test]
    fn range_size_v4_full_space_fits() {
        // Full IPv4 space size = 2^32, representable (and <= the spec's lo bound).
        assert_eq!(
            Ipv4Key::range_size(Ipv4Key::MIN, Ipv4Key::MAX),
            Some(1u128 << 32)
        );
        assert_eq!(Ipv4Key::range_size(Ipv4Key(10), Ipv4Key(10)), Some(1));
    }

    #[test]
    fn range_size_v6_full_space_is_unrepresentable() {
        assert_eq!(Ipv6Key::range_size(Ipv6Key::MIN, Ipv6Key::MAX), None);
        // One short of full is representable (2^128 - 1).
        let almost = Ipv6Key {
            hi: u64::MAX,
            lo: u64::MAX - 1,
        };
        assert_eq!(
            Ipv6Key::range_size(Ipv6Key::MIN, almost),
            Some(u128::MAX) // 2^128 - 1
        );
        assert_eq!(Ipv6Key::range_size(Ipv6Key::MIN, Ipv6Key::MIN), Some(1));
    }
}
