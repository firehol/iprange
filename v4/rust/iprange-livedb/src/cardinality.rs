//! Exact unsigned 129-bit address cardinalities.

use core::fmt;

/// An exact unsigned value in `0..=2^129-1`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Cardinality129 {
    /// Bit 128. Only zero or one is valid.
    bit128: u8,
    /// Bits 64 through 127.
    hi: u64,
    /// Bits 0 through 63.
    lo: u64,
}

/// A checked cardinality operation exceeded its destination type.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CardinalityOverflow;

impl Cardinality129 {
    pub const ZERO: Self = Self {
        bit128: 0,
        hi: 0,
        lo: 0,
    };

    pub const FULL_IPV6_SPACE: Self = Self {
        bit128: 1,
        hi: 0,
        lo: 0,
    };

    #[inline]
    pub const fn try_new(bit128: u8, hi: u64, lo: u64) -> Option<Self> {
        if bit128 <= 1 {
            Some(Self { bit128, hi, lo })
        } else {
            None
        }
    }

    #[inline]
    pub const fn bit128(self) -> u8 {
        self.bit128
    }

    #[inline]
    pub const fn hi(self) -> u64 {
        self.hi
    }

    #[inline]
    pub const fn lo(self) -> u64 {
        self.lo
    }

    #[inline]
    pub const fn from_u64(value: u64) -> Self {
        Self {
            bit128: 0,
            hi: 0,
            lo: value,
        }
    }

    #[inline]
    pub const fn from_u128(value: u128) -> Self {
        Self {
            bit128: 0,
            hi: (value >> 64) as u64,
            lo: value as u64,
        }
    }

    #[inline]
    pub fn checked_add(self, rhs: Self) -> Result<Self, CardinalityOverflow> {
        let (lo, carry_lo) = self.lo.overflowing_add(rhs.lo);
        let (hi0, carry_hi0) = self.hi.overflowing_add(rhs.hi);
        let (hi, carry_hi1) = hi0.overflowing_add(carry_lo as u64);
        let top = self.bit128 as u16 + rhs.bit128 as u16 + carry_hi0 as u16 + carry_hi1 as u16;
        if top > 1 {
            return Err(CardinalityOverflow);
        }
        Ok(Self {
            bit128: top as u8,
            hi,
            lo,
        })
    }

    #[inline]
    pub fn checked_sub(self, rhs: Self) -> Result<Self, CardinalityOverflow> {
        if self < rhs {
            return Err(CardinalityOverflow);
        }
        let (lo, borrow_lo) = self.lo.overflowing_sub(rhs.lo);
        let (hi0, borrow_hi0) = self.hi.overflowing_sub(rhs.hi);
        let (hi, borrow_hi1) = hi0.overflowing_sub(borrow_lo as u64);
        let borrow = borrow_hi0 as u8 + borrow_hi1 as u8;
        let bit128 = self
            .bit128
            .checked_sub(rhs.bit128)
            .and_then(|v| v.checked_sub(borrow))
            .ok_or(CardinalityOverflow)?;
        Ok(Self { bit128, hi, lo })
    }

    /// Exact inclusive IPv4 interval size.
    #[inline]
    pub fn ipv4_inclusive(from: u32, to: u32) -> Result<Self, CardinalityOverflow> {
        if from > to {
            return Err(CardinalityOverflow);
        }
        Ok(Self::from_u64(u64::from(to) - u64::from(from) + 1))
    }

    /// Exact inclusive IPv6 interval size without overflowing at `::/0`.
    #[inline]
    pub fn ipv6_inclusive(
        from_hi: u64,
        from_lo: u64,
        to_hi: u64,
        to_lo: u64,
    ) -> Result<Self, CardinalityOverflow> {
        if (from_hi, from_lo) > (to_hi, to_lo) {
            return Err(CardinalityOverflow);
        }
        let (lo, borrow) = to_lo.overflowing_sub(from_lo);
        let hi = to_hi
            .checked_sub(from_hi)
            .and_then(|v| v.checked_sub(borrow as u64))
            .ok_or(CardinalityOverflow)?;
        Self { bit128: 0, hi, lo }.checked_add(Self::from_u64(1))
    }
}

impl TryFrom<Cardinality129> for u128 {
    type Error = CardinalityOverflow;

    fn try_from(value: Cardinality129) -> Result<Self, Self::Error> {
        if value.bit128 != 0 {
            return Err(CardinalityOverflow);
        }
        Ok((u128::from(value.hi) << 64) | u128::from(value.lo))
    }
}

impl TryFrom<Cardinality129> for u64 {
    type Error = CardinalityOverflow;

    fn try_from(value: Cardinality129) -> Result<Self, Self::Error> {
        if value.bit128 != 0 || value.hi != 0 {
            return Err(CardinalityOverflow);
        }
        Ok(value.lo)
    }
}

impl fmt::Display for Cardinality129 {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut limbs = [self.lo, self.hi, u64::from(self.bit128)];
        if limbs == [0, 0, 0] {
            return f.write_str("0");
        }

        let mut digits = [0u8; 40];
        let mut used = 0usize;
        while limbs != [0, 0, 0] {
            let mut remainder = 0u128;
            for limb in limbs.iter_mut().rev() {
                let dividend = (remainder << 64) | u128::from(*limb);
                *limb = (dividend / 10) as u64;
                remainder = dividend % 10;
            }
            digits[used] = b'0' + remainder as u8;
            used += 1;
        }
        for digit in digits[..used].iter().rev() {
            f.write_str(core::str::from_utf8(core::slice::from_ref(digit)).unwrap())?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::string::ToString;

    #[test]
    fn full_ipv6_space_is_exact() {
        let count = Cardinality129::ipv6_inclusive(0, 0, u64::MAX, u64::MAX).unwrap();
        assert_eq!(count, Cardinality129::FULL_IPV6_SPACE);
        assert_eq!(count.to_string(), "340282366920938463463374607431768211456");
        assert!(u128::try_from(count).is_err());
    }

    #[test]
    fn addition_and_subtraction_are_checked() {
        let max = Cardinality129::try_new(1, u64::MAX, u64::MAX).unwrap();
        assert!(max.checked_add(Cardinality129::from_u64(1)).is_err());
        assert_eq!(
            Cardinality129::FULL_IPV6_SPACE
                .checked_sub(Cardinality129::from_u64(1))
                .unwrap(),
            Cardinality129::from_u128(u128::MAX)
        );
    }

    #[test]
    fn inclusive_boundaries() {
        assert_eq!(
            Cardinality129::ipv4_inclusive(0, u32::MAX).unwrap(),
            Cardinality129::from_u64(1u64 << 32)
        );
        assert_eq!(
            Cardinality129::ipv6_inclusive(5, u64::MAX, 6, 0).unwrap(),
            Cardinality129::from_u64(2)
        );
        assert!(Cardinality129::ipv4_inclusive(2, 1).is_err());
    }

    #[test]
    fn decimal_and_arithmetic_match_u128_below_bit_128() {
        let values = [
            0u128,
            1,
            u64::MAX as u128,
            1u128 << 64,
            (1u128 << 127) + 123_456_789,
            u128::MAX,
        ];
        for value in values {
            let encoded = Cardinality129::from_u128(value);
            assert_eq!(encoded.to_string(), value.to_string());
            assert_eq!(u128::try_from(encoded).unwrap(), value);
        }

        for left in values {
            for right in values {
                let result =
                    Cardinality129::from_u128(left).checked_add(Cardinality129::from_u128(right));
                match left.checked_add(right) {
                    Some(expected) => {
                        assert_eq!(u128::try_from(result.unwrap()).unwrap(), expected)
                    }
                    None => assert!(result.unwrap().bit128() == 1),
                }
            }
        }
    }
}
