//! The fixed `[from, to, scope]` leaf record (§4).
//!
//! `record_size = 2·key_width + scope_width` (§4, D1) — `scope_width` is per-file
//! (from the meta), so a record is handled as a borrowed `record_size`-byte slice, not
//! a fixed-size generic struct. `scope` is **opaque** (D11) and always **borrowed**:
//! the read path never allocates and never copies it.
//!
//! A [`RecordRef`] enforces nothing about ordering/disjointness/`to >= from` — those
//! cross-record invariants are the leaf walk's job (§9). It is purely a zero-copy view.

use core::marker::PhantomData;

use crate::key::IpKey;

/// A zero-copy view over one `record_size`-byte record: `from` (`K::WIDTH`) ·
/// `to` (`K::WIDTH`) · `scope` (the remainder, `scope_width` bytes). The slice length
/// implies `scope_width`; the caller guarantees it is `2·K::WIDTH + scope_width`.
#[derive(Clone, Copy, Debug)]
pub struct RecordRef<'a, K: IpKey> {
    rec: &'a [u8],
    _marker: PhantomData<K>,
}

impl<'a, K: IpKey> RecordRef<'a, K> {
    /// Wrap a record-sized slice. Debug-asserts the minimum length (`2·K::WIDTH`);
    /// the scope is whatever follows.
    #[inline]
    pub fn new(rec: &'a [u8]) -> Self {
        debug_assert!(rec.len() >= 2 * K::WIDTH, "record shorter than two keys");
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

    /// The opaque `scope` bytes (borrowed; never interpreted by the DB — D11). Empty
    /// when `scope_width == 0` (a presence map).
    #[inline]
    pub fn scope(&self) -> &'a [u8] {
        &self.rec[2 * K::WIDTH..]
    }
}

/// Write a record `[from, to, scope]` into `out`, which MUST be exactly
/// `2·K::WIDTH + scope.len()` bytes. Zero-alloc; the caller owns `out`.
#[inline]
pub fn write<K: IpKey>(out: &mut [u8], from: K, to: K, scope: &[u8]) {
    debug_assert_eq!(out.len(), 2 * K::WIDTH + scope.len(), "record buffer size");
    from.write_le(&mut out[0..K::WIDTH]);
    to.write_le(&mut out[K::WIDTH..2 * K::WIDTH]);
    out[2 * K::WIDTH..].copy_from_slice(scope);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::spec::record_size;

    #[test]
    fn v4_round_trip_with_scope() {
        let sz = record_size(4, 4) as usize; // 12
        let mut buf = [0u8; 12];
        let scope = [0xAA, 0xBB, 0xCC, 0xDD];
        write::<Ipv4Key>(&mut buf[..sz], Ipv4Key(0x0a00_0000), Ipv4Key(0x0a00_00ff), &scope);
        let r = RecordRef::<Ipv4Key>::new(&buf[..sz]);
        assert_eq!(r.from(), Ipv4Key(0x0a00_0000));
        assert_eq!(r.to(), Ipv4Key(0x0a00_00ff));
        assert_eq!(r.scope(), &scope);
    }

    #[test]
    fn v6_round_trip_with_scope() {
        let sz = record_size(16, 1) as usize; // 33
        let mut buf = vec![0u8; sz];
        let from = Ipv6Key {
            hi: 0x2001_0db8_0000_0000,
            lo: 0,
        };
        let to = Ipv6Key {
            hi: 0x2001_0db8_0000_0000,
            lo: 0xffff,
        };
        write::<Ipv6Key>(&mut buf, from, to, &[0x7F]);
        let r = RecordRef::<Ipv6Key>::new(&buf);
        assert_eq!(r.from(), from);
        assert_eq!(r.to(), to);
        assert_eq!(r.scope(), &[0x7F]);
    }

    #[test]
    fn scope_width_zero_is_empty() {
        let sz = record_size(4, 0) as usize; // 8
        let mut buf = [0u8; 8];
        write::<Ipv4Key>(&mut buf[..sz], Ipv4Key(1), Ipv4Key(2), &[]);
        let r = RecordRef::<Ipv4Key>::new(&buf[..sz]);
        assert_eq!(r.from(), Ipv4Key(1));
        assert_eq!(r.to(), Ipv4Key(2));
        assert!(r.scope().is_empty(), "presence map: zero-width scope");
    }
}
