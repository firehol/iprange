//! Multi-feed **merge constructor** (v3.1, §13.3): take N value-less single-feed
//! inputs and produce one interleaved merged file whose interval map tags each range
//! with the set of `feed_id`s covering it, plus an in-file `catalog` (kind 4) mapping
//! each `feed_id` to its identity.
//!
//! This is a deterministic, fully-determined operation (the feed-set *is* the overlap
//! result — there is no value conflict to resolve), so it lives in the format library
//! and is distinct from the general set-algebra engine. It reuses the single-feed
//! [`Writer`] for the final sort/coalesce/intern/assemble pass, so the byte-identity
//! contract is shared: the sweep emits one record per elementary interval and the
//! writer canonicalizes (interns membership sets, coalesces any contiguous same-set
//! runs — e.g. a feed's adjacent ranges that the sweep split at their join).
//!
//! Requires the `alloc` feature.

extern crate alloc;
use alloc::collections::BTreeMap;
use alloc::vec::Vec;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::spec;
use crate::writer::{FeedMeta, Value, Writer};

/// One input feed: a stable `feed_id`, its identity, and a **value-less** range list
/// (a plain IP set — the membership set, not a per-range value, is what the sweep
/// assigns, §13.3).
#[derive(Clone, Debug)]
pub struct MergeFeed<K: IpKey> {
    /// The feed's stable, caller-assigned id (§13.2 / decision 3a).
    pub feed_id: u32,
    /// The feed's identity (the six §7 fields), written into the catalog.
    pub meta: FeedMeta,
    /// The feed's ranges (inclusive `[start, end]`), value-less.
    pub ranges: Vec<(K, K)>,
}

/// Builds a v3.1 merged file for key width `K`. The target address family is `K`
/// itself (§13.3): an `Ipv4Key` writer is all-IPv4, an `Ipv6Key` writer all-IPv6 —
/// fixed even when every feed is empty, so there is no vacuous-family ambiguity.
#[derive(Debug)]
pub struct MergeWriter<K: IpKey> {
    /// The merged artifact's own identity (distinct from the per-feed catalog, §13.1).
    feed_meta: FeedMeta,
    license_flags: u32,
    generation_unixtime: u64,
    feeds: Vec<MergeFeed<K>>,
}

impl<K: IpKey> MergeWriter<K> {
    /// Start a merge with the given merged-artifact identity metadata.
    pub fn new(feed_meta: FeedMeta, license_flags: u32, generation_unixtime: u64) -> Self {
        MergeWriter {
            feed_meta,
            license_flags,
            generation_unixtime,
            feeds: Vec::new(),
        }
    }

    /// Add an input feed. `ranges` may overlap ranges of *other* feeds arbitrarily
    /// (that is the point); overlap *within* one feed is rejected at
    /// [`build`](Self::build).
    pub fn add_feed(&mut self, feed_id: u32, meta: FeedMeta, ranges: Vec<(K, K)>) -> Result<()> {
        for (s, e) in &ranges {
            if s > e {
                return Err(Error::InvalidInput("merge feed range start > end"));
            }
        }
        self.feeds.push(MergeFeed {
            feed_id,
            meta,
            ranges,
        });
        Ok(())
    }

    /// Produce the complete v3.1 merged file bytes, or an error if the input is not
    /// encodable (§13.3).
    pub fn build(mut self) -> Result<Vec<u8>> {
        if self.feeds.is_empty() {
            return Err(Error::InvalidInput("a merged file needs at least one feed"));
        }

        // Catalog is sorted by feed_id (strictly ascending, §13.2); reject duplicates.
        self.feeds.sort_by_key(|f| f.feed_id);
        for w in self.feeds.windows(2) {
            if w[0].feed_id == w[1].feed_id {
                return Err(Error::InvalidInput("duplicate feed_id in merge input"));
            }
        }
        // Each catalog identity field is a u32 length on disk (§13.2).
        for feed in &self.feeds {
            feed.meta.check_field_lengths()?;
        }

        // Per-feed: sort by start and reject within-feed overlap (§13.3 — "normalized
        // as the §9 single-feed writer"). Contiguity within a feed is left to the
        // writer's final coalescing pass.
        for feed in &mut self.feeds {
            feed.ranges.sort_by(|a, b| a.0.cmp(&b.0));
            for w in feed.ranges.windows(2) {
                if w[0].1 >= w[1].0 {
                    return Err(Error::InvalidInput("overlapping ranges within one feed"));
                }
            }
        }

        // Boundary sweep (§13.3). Each range contributes a `start` (activate) and an
        // `end + 1` (deactivate) boundary; a range reaching the family maximum has no
        // `end + 1` (it stays active to the last address). Counts make the active set
        // independent of intra-address event order (a feed that ends at `b-1` and
        // resumes at `b` nets to "still active").
        let mut events: BTreeMap<K, (Vec<u32>, Vec<u32>)> = BTreeMap::new();
        for feed in &self.feeds {
            for &(s, e) in &feed.ranges {
                events.entry(s).or_default().0.push(feed.feed_id);
                if let Some(e1) = e.checked_inc() {
                    events.entry(e1).or_default().1.push(feed.feed_id);
                }
            }
        }

        let boundaries: Vec<K> = events.keys().copied().collect();
        let mut active: BTreeMap<u32, u32> = BTreeMap::new(); // feed_id -> active count
        let mut writer = Writer::<K>::new(
            self.feed_meta.clone(),
            self.license_flags,
            self.generation_unixtime,
        );

        for i in 0..boundaries.len() {
            let b = boundaries[i];
            let (adds, removes) = &events[&b];
            for &id in removes {
                if let Some(c) = active.get_mut(&id) {
                    *c -= 1;
                    if *c == 0 {
                        active.remove(&id);
                    }
                }
            }
            for &id in adds {
                *active.entry(id).or_insert(0) += 1;
            }
            if active.is_empty() {
                continue; // a gap covered by no feed — emit nothing
            }
            // Elementary interval [b, end]: end is the predecessor of the next
            // boundary, or the family maximum for the terminal interval.
            let end = if i + 1 < boundaries.len() {
                // next boundary > b >= MIN, so its predecessor always exists.
                boundaries[i + 1]
                    .checked_dec()
                    .ok_or(Error::Invariant("merge boundary underflow"))?
            } else {
                K::MAX
            };
            // active.keys() iterates ascending — the §10 membership-set order.
            let value = membership_value(active.keys().copied());
            writer.add_range(b, end, Some(value))?;
        }

        let catalog = self.encode_catalog();
        // version_minor = 1 + the catalog section mark the file merged (§13.1).
        writer.build_inner(spec::VERSION_MINOR_MERGED, Some(catalog))
    }

    /// Encode the catalog section (§13.2): `feed_count`, `field_count` (6), then each
    /// feed in ascending `feed_id` order as `feed_id` + its six length-prefixed fields.
    fn encode_catalog(&self) -> Vec<u8> {
        let mut out = Vec::new();
        out.extend_from_slice(&(self.feeds.len() as u32).to_le_bytes());
        out.extend_from_slice(&spec::FEED_META_FIELD_COUNT.to_le_bytes());
        for feed in &self.feeds {
            out.extend_from_slice(&feed.feed_id.to_le_bytes());
            feed.meta.encode_fields(&mut out);
        }
        out
    }
}

/// Build a `type_id == 1` membership value from an ascending iterator of `feed_id`s
/// (each as a little-endian `u32`, §10).
fn membership_value(ids: impl Iterator<Item = u32>) -> Value {
    let mut bytes = Vec::new();
    for id in ids {
        bytes.extend_from_slice(&id.to_le_bytes());
    }
    Value { type_id: 1, bytes }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::Ipv4Key;
    use crate::reader::Reader;
    use crate::spec::SectionKind;
    use crate::wire::Header;

    fn meta(name: &str) -> FeedMeta {
        FeedMeta {
            name: name.into(),
            ..Default::default()
        }
    }

    fn r(a: u32, b: u32) -> (Ipv4Key, Ipv4Key) {
        (Ipv4Key(a), Ipv4Key(b))
    }

    /// Decode a membership value's bytes into ascending feed-ids.
    fn ids(bytes: &[u8]) -> Vec<u32> {
        bytes
            .chunks_exact(4)
            .map(|c| u32::from_le_bytes([c[0], c[1], c[2], c[3]]))
            .collect()
    }

    #[test]
    fn partial_overlap_three_intervals() {
        // Feed A=[1,49]∪? ; classic A=[0,10], B=[5,15] split.
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 1_700_000_000);
        m.add_feed(1, meta("a"), vec![r(0, 10)]).unwrap();
        m.add_feed(5, meta("b"), vec![r(5, 15)]).unwrap();
        let bytes = m.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.version_minor, spec::VERSION_MINOR_MERGED);
        assert_eq!(h.entry_count, 3, "{{A}}, {{A,B}}, {{B}}");
        assert_eq!(h.unique_ip_count_lo, 16, "union 0..=15");

        let rd = Reader::open(&bytes).unwrap();
        // [0,4] -> {1}; [5,10] -> {1,5}; [11,15] -> {5}; 16 -> none.
        let v = |ip: u32| {
            let hit = rd.lookup_v4(Ipv4Key(ip)).unwrap();
            hit.map(|h| ids(rd.value(h.value_id).unwrap().bytes))
        };
        assert_eq!(v(0), Some(vec![1]));
        assert_eq!(v(4), Some(vec![1]));
        assert_eq!(v(5), Some(vec![1, 5]));
        assert_eq!(v(10), Some(vec![1, 5]));
        assert_eq!(v(11), Some(vec![5]));
        assert_eq!(v(15), Some(vec![5]));
        assert_eq!(v(16), None);
    }

    #[test]
    fn contiguous_within_feed_coalesces() {
        // A feed split across two contiguous ranges collapses back to one record.
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(7, meta("a"), vec![r(0, 5), r(6, 10)]).unwrap();
        let bytes = m.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 1, "[0,5]+[6,10] coalesce to [0,10]");
        assert_eq!(h.unique_ip_count_lo, 11);
    }

    #[test]
    fn terminal_interval_at_family_max() {
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(2, meta("a"), vec![r(0xFFFF_FFFE, 0xFFFF_FFFF)])
            .unwrap();
        let bytes = m.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(
            h.entry_count, 1,
            "feed reaching family-max emits its record"
        );
        let rd = Reader::open(&bytes).unwrap();
        let hit = rd.lookup_v4(Ipv4Key(0xFFFF_FFFF)).unwrap().unwrap();
        assert_eq!(ids(rd.value(hit.value_id).unwrap().bytes), vec![2]);
    }

    #[test]
    fn empty_merged_file_has_catalog_no_index_records() {
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(1, meta("a"), vec![]).unwrap(); // a feed with no ranges
        let bytes = m.build().unwrap();
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 0);
        assert_eq!(h.version_minor, spec::VERSION_MINOR_MERGED);
        // directory: feed-meta, index, catalog, signature (no values section).
        assert_eq!(h.directory_count, 4);
        let rd = Reader::open(&bytes).unwrap();
        assert_eq!(rd.catalog().unwrap().len(), 1);
    }

    #[test]
    fn membership_dedup_one_value() {
        // Two disjoint stretches both covered only by {3} share one interned value.
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(3, meta("a"), vec![r(0, 10)]).unwrap();
        m.add_feed(9, meta("b"), vec![r(5, 5)]).unwrap();
        let bytes = m.build().unwrap();
        // records: [0,4]={3}, [5,5]={3,9}, [6,10]={3} -> {3} interned once.
        let h = Header::decode(&bytes).unwrap();
        assert_eq!(h.entry_count, 3);
        let _ = SectionKind::Catalog; // keep import used
    }

    #[test]
    fn reject_empty_no_feeds() {
        let m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        assert!(matches!(m.build(), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn reject_duplicate_feed_id() {
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(1, meta("a"), vec![r(0, 10)]).unwrap();
        m.add_feed(1, meta("b"), vec![r(20, 30)]).unwrap();
        assert!(matches!(m.build(), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn reject_within_feed_overlap() {
        let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 0);
        m.add_feed(1, meta("a"), vec![r(0, 10), r(5, 15)]).unwrap();
        assert!(matches!(m.build(), Err(Error::InvalidInput(_))));
    }

    #[test]
    fn deterministic_byte_identical() {
        let mk = || {
            let mut m = MergeWriter::<Ipv4Key>::new(meta("merged"), 0, 42);
            // add feeds out of order — output must not depend on input order.
            m.add_feed(5, meta("b"), vec![r(5, 15)]).unwrap();
            m.add_feed(1, meta("a"), vec![r(0, 10)]).unwrap();
            m.build().unwrap()
        };
        assert_eq!(mk(), mk());
    }
}
