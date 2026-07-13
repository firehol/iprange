//! The fixed `[from: K, to: K, scope_id: u32]` leaf record.
//!
//! Every record is exactly `2·K::WIDTH + 4` bytes — 12 (IPv4) or 36 (IPv6).
//! `scope_id` is a 4-byte little-endian u32; its interpretation depends on the
//! file's `scope_mode` (scalar / bitmap / indirect), which the engine never
//! inspects — it is opaque to the B+tree.

use core::marker::PhantomData;

use crate::key::IpKey;
use crate::spec;
use crate::wire;

/// A zero-copy view over one `(2·K::WIDTH + 4)`-byte record.
#[derive(Clone, Copy, Debug)]
pub struct RecordRef<'a, K: IpKey> {
    rec: &'a [u8],
    _marker: PhantomData<K>,
}

/// Record size for key type K: `2·K::WIDTH + 4`.
pub const fn record_size<K: IpKey>() -> usize {
    2 * K::WIDTH + spec::SCOPE_ID_SIZE as usize
}

impl<'a, K: IpKey> RecordRef<'a, K> {
    /// Wrap a record-sized slice.
    #[inline]
    pub fn new(rec: &'a [u8]) -> Self {
        debug_assert_eq!(rec.len(), record_size::<K>(), "record size mismatch");
        RecordRef {
            rec,
            _marker: PhantomData,
        }
    }

    /// The inclusive range start `from`.
    #[inline]
    pub fn from(&self) -> K {
        K::read_le(&self.rec[0..K::WIDTH])
    }

    /// The inclusive range end `to`.
    #[inline]
    pub fn to(&self) -> K {
        K::read_le(&self.rec[K::WIDTH..2 * K::WIDTH])
    }

    /// The 4-byte `scope_id` (little-endian u32). Its interpretation is opaque
    /// to the engine — scalar, bitmap, or indirect-pointer, per `scope_mode`.
    #[inline]
    pub fn scope_id(&self) -> u32 {
        wire::u32_le(self.rec, 2 * K::WIDTH)
    }
}

/// Write a record `[from, to, scope_id]` into `out`, which MUST be exactly
/// `2·K::WIDTH + 4` bytes. Zero-alloc; the caller owns `out`.
#[inline]
pub fn write<K: IpKey>(out: &mut [u8], from: K, to: K, scope_id: u32) {
    debug_assert_eq!(out.len(), record_size::<K>(), "record buffer size");
    from.write_le(&mut out[0..K::WIDTH]);
    to.write_le(&mut out[K::WIDTH..2 * K::WIDTH]);
    wire::put_u32(out, 2 * K::WIDTH, scope_id);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};

    #[test]
    fn v4_round_trip() {
        let mut buf = [0u8; 12]; // 2*4 + 4
        write::<Ipv4Key>(&mut buf, Ipv4Key(0x0a00_0000), Ipv4Key(0x0a00_00ff), 42);
        let r = RecordRef::<Ipv4Key>::new(&buf);
        assert_eq!(r.from(), Ipv4Key(0x0a00_0000));
        assert_eq!(r.to(), Ipv4Key(0x0a00_00ff));
        assert_eq!(r.scope_id(), 42);
    }

    #[test]
    fn v6_round_trip() {
        let mut buf = [0u8; 36]; // 2*16 + 4
        let from = Ipv6Key {
            hi: 0x2001_0db8_0000_0000,
            lo: 0,
        };
        let to = Ipv6Key {
            hi: 0x2001_0db8_0000_0000,
            lo: 0xffff,
        };
        write::<Ipv6Key>(&mut buf, from, to, 0xDEAD_BEEF);
        let r = RecordRef::<Ipv6Key>::new(&buf);
        assert_eq!(r.from(), from);
        assert_eq!(r.to(), to);
        assert_eq!(r.scope_id(), 0xDEAD_BEEF);
    }

    #[test]
    fn record_sizes() {
        assert_eq!(record_size::<Ipv4Key>(), 12);
        assert_eq!(record_size::<Ipv6Key>(), 36);
    }
}
