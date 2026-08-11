//! Zero-allocation typed reads over one pinned structured generation.

use std::fmt;

use crate::contract::{AddressFamily, MetaV4, StructureKind, ValueKind};
use crate::error::{Error, Result};
use crate::format::Generation;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::membership_view::{self, MembershipView};
use crate::process_identity::ProcessIdentity;
use crate::range_tree;
use crate::reader_core::MembershipToken;

use super::network_enrichment_v1::Codec;
use super::{table, NetworkEnrichmentV1};

/// Typed enrichment tied to the reader generation that resolved it.
pub struct NetworkEnrichmentV1View<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    value: NetworkEnrichmentV1,
    membership_id: u32,
    owner_identity: Option<ProcessIdentity>,
}

impl NetworkEnrichmentV1View<'_> {
    /// Copy the fixed scalar fields without resolving threat membership.
    pub const fn value(&self) -> NetworkEnrichmentV1 {
        self.value
    }

    /// Resolve the optional threat bitmap only when requested.
    pub fn threat_membership(&self) -> Result<Option<MembershipView<'_>>> {
        self.require_owner()?;
        if self.membership_id == 0 {
            return Ok(None);
        }
        membership_view::by_id(
            self.mapping,
            &self.meta,
            self.membership_id,
            self.owner_identity,
        )
        .map(Some)
    }

    pub(crate) const fn threat_membership_token(&self) -> Option<MembershipToken> {
        if self.membership_id == 0 {
            None
        } else {
            Some(MembershipToken::new(self.membership_id))
        }
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_identity.is_some_and(|owner| !owner.is_current()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

impl fmt::Debug for NetworkEnrichmentV1View<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("NetworkEnrichmentV1View")
            .field("value", &self.value)
            .field("has_threat_membership", &(self.membership_id != 0))
            .finish()
    }
}

pub(crate) fn lookup_v4<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    address: Ipv4Key,
    owner_identity: Option<ProcessIdentity>,
) -> Result<Option<NetworkEnrichmentV1View<'a>>> {
    require_kind(meta, AddressFamily::Ipv4)?;
    lookup(
        mapping,
        meta,
        range_tree::lookup(mapping, meta, address)?,
        owner_identity,
    )
}

pub(crate) fn lookup_v6<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    address: Ipv6Key,
    owner_identity: Option<ProcessIdentity>,
) -> Result<Option<NetworkEnrichmentV1View<'a>>> {
    require_kind(meta, AddressFamily::Ipv6)?;
    lookup(
        mapping,
        meta,
        range_tree::lookup(mapping, meta, address)?,
        owner_identity,
    )
}

fn lookup<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    id: Option<u32>,
    owner_identity: Option<ProcessIdentity>,
) -> Result<Option<NetworkEnrichmentV1View<'a>>> {
    let Some(id) = id else {
        return Ok(None);
    };
    let generation = Generation::new(mapping, *meta);
    let decoded = table::inspect::<Codec, _, _, _>(
        &generation,
        meta.structure_id_root,
        meta.structure_id_limit,
        id,
        |payload| Ok(Codec::decode_mapped(payload)),
    )?
    .ok_or_else(|| Error::corrupt("range names an absent structure ID"))?;
    Ok(Some(NetworkEnrichmentV1View {
        mapping,
        meta: *meta,
        value: decoded.0,
        membership_id: decoded.1,
        owner_identity,
    }))
}

pub(crate) fn by_id<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    id: u32,
    owner_identity: Option<ProcessIdentity>,
) -> Result<NetworkEnrichmentV1View<'a>> {
    require_kind(meta, meta.address_family)?;
    lookup(mapping, meta, Some(id), owner_identity)?
        .ok_or_else(|| Error::corrupt("range names an absent structure ID"))
}

pub(crate) fn membership_id(mapping: &Mapping, meta: &MetaV4, id: u32) -> Result<u32> {
    require_kind(meta, meta.address_family)?;
    let generation = Generation::new(mapping, *meta);
    table::inspect::<Codec, _, _, _>(
        &generation,
        meta.structure_id_root,
        meta.structure_id_limit,
        id,
        |payload| Ok(Codec::decode_mapped(payload).1),
    )?
    .ok_or_else(|| Error::corrupt("range names an absent structure ID"))
}

pub(crate) fn require_kind(meta: &MetaV4, family: AddressFamily) -> Result<()> {
    if meta.value_kind != ValueKind::Structured
        || meta.structure_kind() != Some(StructureKind::NetworkEnrichmentV1)
    {
        return Err(Error::WrongStructureKind(
            "network enrichment lookup requires its matching structured database",
        ));
    }
    if meta.address_family != family {
        return Err(Error::WrongAddressFamily(
            "lookup address family does not match the database",
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::fs::{self, OpenOptions};
    use std::path::{Path, PathBuf};

    use super::*;
    use crate::contract::ValueTag;
    use crate::database::ImmutableReader;
    use crate::feed::FeedName;
    use crate::immutable_output::{Builder, MembershipWords, OutputBudget, OutputSpec};
    use crate::test_alloc::count_thread_allocations;

    struct TestPath(PathBuf);

    impl TestPath {
        fn new() -> Self {
            Self(crate::test_support_tests::unique_path(
                "iprange-v4-structured-view",
            ))
        }
    }

    impl Drop for TestPath {
        fn drop(&mut self) {
            let _ = fs::remove_file(&self.0);
        }
    }

    struct Words([u64; 1]);

    impl MembershipWords for Words {
        fn word_count(&self) -> u32 {
            1
        }

        fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
            let start = start as usize;
            output.copy_from_slice(&self.0[start..start + output.len()]);
            Ok(())
        }
    }

    #[test]
    fn lookup_does_only_required_work_and_membership_stays_lazy() {
        let path = TestPath::new();
        build_fixture(&path.0);
        let reader = ImmutableReader::open(&path.0).unwrap();

        let warmed = reader
            .lookup_network_enrichment_v1_v4(Ipv4Key(5))
            .unwrap()
            .unwrap();
        assert!(warmed
            .threat_membership()
            .unwrap()
            .unwrap()
            .contains_index(3)
            .unwrap());

        let ((view, work), allocations) = count_thread_allocations(|| {
            crate::work::measure(|| reader.lookup_network_enrichment_v1_v4(Ipv4Key(5)))
        });
        let view = view.unwrap().unwrap();
        assert_eq!(allocations, 0);
        assert_eq!(view.value().asn, 64512);
        assert_eq!(work.tree_lookups, 1);
        assert_eq!(work.structure_lookups, 1);
        assert_eq!(work.structure_decodes, 1);
        assert_eq!(work.membership_lookups, 0);
        assert_eq!(work.membership_word_reads, 0);
        assert_eq!(work.pages_visited, 2);

        let ((membership, work), allocations) =
            count_thread_allocations(|| crate::work::measure(|| view.threat_membership()));
        let membership = membership.unwrap().unwrap();
        assert_eq!(allocations, 0);
        assert_eq!(work.tree_lookups, 1);
        assert_eq!(work.membership_lookups, 1);
        assert_eq!(work.structure_lookups, 0);
        assert_eq!(work.membership_word_reads, 0);
        assert_eq!(work.pages_visited, 1);

        let ((contains, work), allocations) =
            count_thread_allocations(|| crate::work::measure(|| membership.contains_index(3)));
        assert!(contains.unwrap());
        assert_eq!(allocations, 0);
        assert_eq!(work.tree_lookups, 0);
        assert_eq!(work.membership_lookups, 0);
        assert_eq!(work.membership_word_reads, 1);
    }

    fn build_fixture(path: &Path) {
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(path)
            .unwrap();
        let mut output = Builder::new_owned(
            file,
            OutputSpec {
                address_family: AddressFamily::Ipv4,
                value_kind: ValueKind::Structured,
                structure_kind: StructureKind::NetworkEnrichmentV1,
                value_tag: ValueTag::new(b"enrichment").unwrap(),
                database_id: [1; 16],
                transaction_id: 1,
                commit_nonce: [2; 16],
                feed_index_limit: 64,
            },
            OutputBudget {
                max_output_pages: 100,
            },
        )
        .unwrap();
        output
            .push_feed(FeedName::new("threat").unwrap(), 3)
            .unwrap();
        output
            .push_network_enrichment_v1_v4(
                Ipv4Key(0),
                Ipv4Key(9),
                NetworkEnrichmentV1 {
                    asn: 64512,
                    ..NetworkEnrichmentV1::default()
                },
                Some(&Words([1 << 3])),
            )
            .unwrap();
        drop(output.finish_owned().unwrap());
    }
}
