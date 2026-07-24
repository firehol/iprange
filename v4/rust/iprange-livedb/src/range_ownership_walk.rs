//! Bounded ordinary-path ownership walk for a selected range tree.
//!
//! This is preparation scratch for retirement, not explicit validation. It
//! checks only the local facts needed to safely discover selected pages.

use crate::contract::{MetaV4, MAX_TREE_LEVEL, PAGE_SIZE};
use crate::key::IpKey;
use crate::page::{PageHeader, PageType};
use crate::page_number_index::{PageNumberIndex, PageNumberIndexError};
use crate::page_source::{CommittedPageSource, PageSourceError};
use crate::range_page::{RangeBranch, RangeLeaf, RangePageError};

const PATH_CAPACITY: usize = MAX_TREE_LEVEL as usize + 1;
const ROOT_LEVEL: u16 = u16::MAX;

#[derive(Clone, Copy, Debug)]
struct RangeOwnershipFrame {
    pgno: u32,
    expected_level: u16,
    next_child: usize,
    loaded: bool,
}

impl RangeOwnershipFrame {
    const EMPTY: Self = Self {
        pgno: 0,
        expected_level: 0,
        next_child: 0,
        loaded: false,
    };
}

/// Fixed caller-owned control storage for one complete selected-range walk.
/// Its pages are logical scratch and never carry v4 physical page identities.
pub(crate) struct RangeTreeOwnershipScratch {
    pages: [[u8; PAGE_SIZE]; PATH_CAPACITY],
    frames: [RangeOwnershipFrame; PATH_CAPACITY],
}

impl RangeTreeOwnershipScratch {
    pub(crate) const fn new() -> Self {
        Self {
            pages: [[0; PAGE_SIZE]; PATH_CAPACITY],
            frames: [RangeOwnershipFrame::EMPTY; PATH_CAPACITY],
        }
    }

    fn reset(&mut self) {
        self.frames.fill(RangeOwnershipFrame::EMPTY);
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangeOwnershipWalkError {
    Source(PageSourceError),
    Page {
        page: u32,
        error: RangePageError,
    },
    WrongKeyFamily,
    RootRecordCount,
    RootOutOfBounds(u32),
    RootType {
        page: u32,
        page_type: PageType,
    },
    ChildType {
        page: u32,
        page_type: PageType,
    },
    ChildLevel {
        page: u32,
        expected: u16,
        actual: u16,
    },
    WorkBudget {
        required: u64,
        actual: u64,
    },
    Index(PageNumberIndexError),
}

fn next_work(work: u64) -> u64 {
    work.saturating_add(1)
}

/// Add every selected reachable range branch and leaf to `index`.
///
/// Empty children are still reachable and are therefore included. The work
/// limit bounds alias-induced repeated traversal without making this an
/// implicit global alias/CRC/fence validation pass.
pub(crate) fn collect_range_tree_ownership<K: IpKey, S: CommittedPageSource + ?Sized>(
    source: &S,
    meta: MetaV4,
    index: &mut PageNumberIndex<'_, '_>,
    scratch: &mut RangeTreeOwnershipScratch,
    max_work: u64,
) -> Result<u64, RangeOwnershipWalkError> {
    if K::FAMILY != meta.address_family {
        return Err(RangeOwnershipWalkError::WrongKeyFamily);
    }
    if meta.range_root == 0 {
        return if meta.range_record_count == 0 {
            Ok(0)
        } else {
            Err(RangeOwnershipWalkError::RootRecordCount)
        };
    }
    if meta.range_root < 2 || u64::from(meta.range_root) >= meta.page_count {
        return Err(RangeOwnershipWalkError::RootOutOfBounds(meta.range_root));
    }
    if max_work == 0 {
        return Err(RangeOwnershipWalkError::WorkBudget {
            required: 1,
            actual: 0,
        });
    }
    source
        .check_access()
        .map_err(RangeOwnershipWalkError::Source)?;

    scratch.reset();
    scratch.frames[0] = RangeOwnershipFrame {
        pgno: meta.range_root,
        expected_level: ROOT_LEVEL,
        next_child: 0,
        loaded: false,
    };
    let mut depth = 0usize;
    let mut work = 0u64;

    loop {
        if !scratch.frames[depth].loaded {
            if work >= max_work {
                return Err(RangeOwnershipWalkError::WorkBudget {
                    required: next_work(work),
                    actual: max_work,
                });
            }
            let pgno = scratch.frames[depth].pgno;
            source
                .read_page(pgno, &mut scratch.pages[depth])
                .map_err(RangeOwnershipWalkError::Source)?;
            let header = PageHeader::decode(&scratch.pages[depth], meta.txn_id)
                .map_err(RangePageError::from)
                .map_err(|error| RangeOwnershipWalkError::Page { page: pgno, error })?;

            match header.page_type {
                PageType::RangeLeaf => {
                    let expected_level = scratch.frames[depth].expected_level;
                    if expected_level != ROOT_LEVEL && expected_level != 0 {
                        return Err(RangeOwnershipWalkError::ChildType {
                            page: pgno,
                            page_type: header.page_type,
                        });
                    }
                    RangeLeaf::<K>::open(
                        &scratch.pages[depth],
                        meta.txn_id,
                        meta.address_family,
                        meta.value_kind,
                    )
                    .map_err(|error| RangeOwnershipWalkError::Page { page: pgno, error })?;
                    index.insert(pgno).map_err(RangeOwnershipWalkError::Index)?;
                    work += 1;
                    if depth == 0 {
                        return Ok(work);
                    }
                    depth -= 1;
                    continue;
                }
                PageType::RangeBranch => {
                    let branch = RangeBranch::<K>::open(
                        &scratch.pages[depth],
                        meta.txn_id,
                        meta.address_family,
                        meta.page_count,
                    )
                    .map_err(|error| RangeOwnershipWalkError::Page { page: pgno, error })?;
                    let expected_level = scratch.frames[depth].expected_level;
                    if expected_level == ROOT_LEVEL {
                        scratch.frames[depth].expected_level = branch.level;
                    } else if expected_level != branch.level {
                        return Err(RangeOwnershipWalkError::ChildLevel {
                            page: pgno,
                            expected: expected_level,
                            actual: branch.level,
                        });
                    }
                    index.insert(pgno).map_err(RangeOwnershipWalkError::Index)?;
                    work += 1;
                    scratch.frames[depth].loaded = true;
                    scratch.frames[depth].next_child = 0;
                }
                page_type => {
                    return if depth == 0 {
                        Err(RangeOwnershipWalkError::RootType {
                            page: pgno,
                            page_type,
                        })
                    } else {
                        Err(RangeOwnershipWalkError::ChildType {
                            page: pgno,
                            page_type,
                        })
                    };
                }
            }
        }

        let (pgno, expected_level, next_child) = {
            let frame = scratch.frames[depth];
            (frame.pgno, frame.expected_level, frame.next_child)
        };
        let branch = RangeBranch::<K>::open(
            &scratch.pages[depth],
            meta.txn_id,
            meta.address_family,
            meta.page_count,
        )
        .map_err(|error| RangeOwnershipWalkError::Page { page: pgno, error })?;
        if next_child == branch.len() {
            if depth == 0 {
                return Ok(work);
            }
            depth -= 1;
            continue;
        }
        let entry = branch
            .entry(next_child)
            .map_err(|error| RangeOwnershipWalkError::Page { page: pgno, error })?;
        scratch.frames[depth].next_child += 1;
        if depth + 1 == PATH_CAPACITY || expected_level == 0 {
            return Err(RangeOwnershipWalkError::ChildLevel {
                page: entry.child_pgno,
                expected: 0,
                actual: expected_level,
            });
        }
        depth += 1;
        scratch.frames[depth] = RangeOwnershipFrame {
            pgno: entry.child_pgno,
            expected_level: expected_level - 1,
            next_child: 0,
            loaded: false,
        };
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::contract::AddressFamily;
    use crate::key::Ipv4Key;
    use crate::page::write_crc32c;
    use crate::page_source::SlicePageSource;
    use crate::range_page::{encode_branch, encode_leaf, RangeBranchEntry, RangeRecord};
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    fn page_mut(bytes: &mut [u8], page: u32) -> &mut [u8; PAGE_SIZE] {
        let start = page as usize * PAGE_SIZE;
        let page = &mut bytes[start..start + PAGE_SIZE];
        page.try_into().unwrap()
    }

    fn meta(page_count: u64, root: u32, records: u64) -> MetaV4 {
        let mut meta = empty_direct_meta(3);
        meta.page_count = page_count;
        meta.range_root = root;
        meta.range_record_count = records;
        meta
    }

    fn new_index<'workspace, 'storage>(
        workspace: &'workspace mut crate::page_number_index::PageNumberIndexWorkspace<'storage>,
    ) -> PageNumberIndex<'workspace, 'storage> {
        PageNumberIndex::new(workspace).unwrap()
    }

    fn image() -> (Vec<u8>, MetaV4) {
        const PAGE_COUNT: u64 = 12;
        let mut bytes = vec![0; PAGE_COUNT as usize * PAGE_SIZE];
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 8),
            1,
            1,
            PAGE_COUNT,
            Ipv4Key::MIN,
            None,
            &[
                RangeBranchEntry {
                    lower_fence: Ipv4Key(0),
                    child_pgno: 11,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(10),
                    last_from: Ipv4Key(10),
                    last_to: Ipv4Key(20),
                },
                RangeBranchEntry {
                    lower_fence: Ipv4Key(100),
                    child_pgno: 3,
                    subtree_record_count: 0,
                    first_from: Ipv4Key::MIN,
                    last_from: Ipv4Key::MIN,
                    last_to: Ipv4Key::MIN,
                },
                RangeBranchEntry {
                    lower_fence: Ipv4Key(200),
                    child_pgno: 4,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(210),
                    last_from: Ipv4Key(210),
                    last_to: Ipv4Key(220),
                },
            ],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 11),
            1,
            crate::contract::ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 1,
            }],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 3),
            1,
            crate::contract::ValueKind::Direct,
            &[],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 4),
            1,
            crate::contract::ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(210),
                to: Ipv4Key(220),
                value: 2,
            }],
        )
        .unwrap();
        (bytes, meta(PAGE_COUNT, 8, 2))
    }

    fn collect(index: &mut PageNumberIndex<'_, '_>) -> Vec<u32> {
        let mut values = Vec::new();
        index
            .visit_ascending(|value| {
                values.push(value);
                Ok::<(), ()>(())
            })
            .unwrap();
        values
    }

    #[test]
    fn includes_empty_children_and_sorts_physical_pages() {
        let (bytes, meta) = image();
        let source = SlicePageSource::new(&bytes, meta.page_count);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 4];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(&source, meta, &mut index, &mut scratch, 4),
            Ok(4)
        );
        assert_eq!(collect(&mut index), vec![3, 4, 8, 11]);
    }

    #[test]
    fn handles_multilevel_tree() {
        const PAGE_COUNT: u64 = 14;
        let mut bytes = vec![0; PAGE_COUNT as usize * PAGE_SIZE];
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 12),
            1,
            2,
            PAGE_COUNT,
            Ipv4Key::MIN,
            None,
            &[RangeBranchEntry {
                lower_fence: Ipv4Key::MIN,
                child_pgno: 7,
                subtree_record_count: 2,
                first_from: Ipv4Key(10),
                last_from: Ipv4Key(210),
                last_to: Ipv4Key(220),
            }],
        )
        .unwrap();
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 7),
            1,
            1,
            PAGE_COUNT,
            Ipv4Key::MIN,
            None,
            &[
                RangeBranchEntry {
                    lower_fence: Ipv4Key::MIN,
                    child_pgno: 11,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(10),
                    last_from: Ipv4Key(10),
                    last_to: Ipv4Key(20),
                },
                RangeBranchEntry {
                    lower_fence: Ipv4Key(200),
                    child_pgno: 3,
                    subtree_record_count: 1,
                    first_from: Ipv4Key(210),
                    last_from: Ipv4Key(210),
                    last_to: Ipv4Key(220),
                },
            ],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 11),
            1,
            crate::contract::ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 1,
            }],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 3),
            1,
            crate::contract::ValueKind::Direct,
            &[RangeRecord {
                from: Ipv4Key(210),
                to: Ipv4Key(220),
                value: 2,
            }],
        )
        .unwrap();
        let meta = meta(PAGE_COUNT, 12, 2);
        let source = SlicePageSource::new(&bytes, PAGE_COUNT);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 4];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(&source, meta, &mut index, &mut scratch, 4),
            Ok(4)
        );
        assert_eq!(collect(&mut index), vec![3, 7, 11, 12]);
    }

    #[test]
    fn supports_ipv6_leaf() {
        use crate::key::Ipv6Key;

        let mut bytes = vec![0; 3 * PAGE_SIZE];
        encode_leaf::<Ipv6Key>(
            page_mut(&mut bytes, 2),
            1,
            crate::contract::ValueKind::Direct,
            &[RangeRecord {
                from: Ipv6Key {
                    hi: 0x2001_0db8,
                    lo: 1,
                },
                to: Ipv6Key {
                    hi: 0x2001_0db8,
                    lo: 2,
                },
                value: 7,
            }],
        )
        .unwrap();
        let mut meta = meta(3, 2, 1);
        meta.address_family = AddressFamily::Ipv6;
        let source = SlicePageSource::new(&bytes, 3);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 1];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        assert_eq!(
            collect_range_tree_ownership::<Ipv6Key, _>(&source, meta, &mut index, &mut scratch, 1),
            Ok(1)
        );
        assert_eq!(collect(&mut index), vec![2]);
    }

    #[test]
    fn uses_exact_maximum_depth() {
        let page_count = u64::from(MAX_TREE_LEVEL) + 3;
        let mut bytes = vec![0; page_count as usize * PAGE_SIZE];
        for level in (1..=MAX_TREE_LEVEL).rev() {
            let page = 2 + u32::from(MAX_TREE_LEVEL - level);
            encode_branch::<Ipv4Key>(
                page_mut(&mut bytes, page),
                1,
                level,
                page_count,
                Ipv4Key::MIN,
                None,
                &[RangeBranchEntry {
                    lower_fence: Ipv4Key::MIN,
                    child_pgno: page + 1,
                    subtree_record_count: 0,
                    first_from: Ipv4Key::MIN,
                    last_from: Ipv4Key::MIN,
                    last_to: Ipv4Key::MIN,
                }],
            )
            .unwrap();
        }
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, page_count as u32 - 1),
            1,
            crate::contract::ValueKind::Direct,
            &[],
        )
        .unwrap();
        let meta = meta(page_count, 2, 0);
        let source = SlicePageSource::new(&bytes, page_count);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 1];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        let expected = u64::from(MAX_TREE_LEVEL) + 1;
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(
                &source,
                meta,
                &mut index,
                &mut scratch,
                expected,
            ),
            Ok(expected)
        );
        let values = collect(&mut index);
        assert_eq!(values.len(), expected as usize);
        assert_eq!(values.first(), Some(&2));
        assert_eq!(values.last(), Some(&(page_count as u32 - 1)));
    }

    #[test]
    fn rejects_wrong_child_type_and_level() {
        let mut bytes = vec![0; 5 * PAGE_SIZE];
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 2),
            1,
            1,
            5,
            Ipv4Key::MIN,
            None,
            &[RangeBranchEntry {
                lower_fence: Ipv4Key::MIN,
                child_pgno: 3,
                subtree_record_count: 0,
                first_from: Ipv4Key::MIN,
                last_from: Ipv4Key::MIN,
                last_to: Ipv4Key::MIN,
            }],
        )
        .unwrap();
        crate::page::PageHeader {
            page_type: PageType::MetadataChunk,
            born_txn: 1,
            item_count: 0,
            level: 0,
            lower: crate::page::PAGE_HEADER_SIZE,
            upper: PAGE_SIZE as u16,
            aux: AddressFamily::Ipv4 as u32,
            page_crc32c: 0,
        }
        .encode_into(page_mut(&mut bytes, 3));
        write_crc32c(page_mut(&mut bytes, 3));
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 2];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        {
            let source = SlicePageSource::new(&bytes, 5);
            assert!(matches!(
                collect_range_tree_ownership::<Ipv4Key, _>(
                    &source,
                    meta(5, 2, 0),
                    &mut index,
                    &mut scratch,
                    2,
                ),
                Err(RangeOwnershipWalkError::ChildType { .. })
            ));
        }

        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 2),
            1,
            2,
            5,
            Ipv4Key::MIN,
            None,
            &[RangeBranchEntry {
                lower_fence: Ipv4Key::MIN,
                child_pgno: 3,
                subtree_record_count: 0,
                first_from: Ipv4Key::MIN,
                last_from: Ipv4Key::MIN,
                last_to: Ipv4Key::MIN,
            }],
        )
        .unwrap();
        encode_branch::<Ipv4Key>(
            page_mut(&mut bytes, 3),
            1,
            2,
            5,
            Ipv4Key::MIN,
            None,
            &[RangeBranchEntry {
                lower_fence: Ipv4Key::MIN,
                child_pgno: 4,
                subtree_record_count: 0,
                first_from: Ipv4Key::MIN,
                last_from: Ipv4Key::MIN,
                last_to: Ipv4Key::MIN,
            }],
        )
        .unwrap();
        encode_leaf::<Ipv4Key>(
            page_mut(&mut bytes, 4),
            1,
            crate::contract::ValueKind::Direct,
            &[],
        )
        .unwrap();
        index.discard_after_abort();
        let source = SlicePageSource::new(&bytes, 5);
        assert!(matches!(
            collect_range_tree_ownership::<Ipv4Key, _>(
                &source,
                meta(5, 2, 0),
                &mut index,
                &mut scratch,
                3,
            ),
            Err(RangeOwnershipWalkError::ChildLevel { .. })
        ));
    }

    #[test]
    fn bounds_work_and_propagates_source_failure() {
        let (bytes, meta) = image();
        let source = SlicePageSource::new(&bytes, meta.page_count);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 4];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(&source, meta, &mut index, &mut scratch, 2),
            Err(RangeOwnershipWalkError::WorkBudget {
                required: 3,
                actual: 2,
            })
        );
        index.discard_after_abort();
        let truncated = SlicePageSource::new(&bytes[..9 * PAGE_SIZE], meta.page_count);
        assert!(matches!(
            collect_range_tree_ownership::<Ipv4Key, _>(
                &truncated,
                meta,
                &mut index,
                &mut scratch,
                4
            ),
            Err(RangeOwnershipWalkError::Source(
                PageSourceError::ShortRead { .. }
            ))
        ));
    }

    #[test]
    fn handles_empty_root_and_uses_no_heap_after_setup() {
        let empty = meta(2, 0, 0);
        let source = SlicePageSource::new(&[], 2);
        let mut pages = [crate::page_number_index::PageNumberIndexPage::empty(); 4];
        let mut workspace = crate::page_number_index::PageNumberIndexWorkspace::new(&mut pages);
        let mut index = new_index(&mut workspace);
        let mut scratch = RangeTreeOwnershipScratch::new();
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(&source, empty, &mut index, &mut scratch, 1),
            Ok(0)
        );
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(
                &source,
                meta(2, 0, 1),
                &mut index,
                &mut scratch,
                1,
            ),
            Err(RangeOwnershipWalkError::RootRecordCount)
        );

        let (bytes, meta) = image();
        let source = SlicePageSource::new(&bytes, meta.page_count);
        index.discard_after_abort();
        assert_eq!(
            collect_range_tree_ownership::<Ipv4Key, _>(&source, meta, &mut index, &mut scratch, 4),
            Ok(4)
        );
        let (result, allocations) = count_thread_allocations(|| {
            index.discard_after_abort();
            collect_range_tree_ownership::<Ipv4Key, _>(&source, meta, &mut index, &mut scratch, 4)
        });
        assert_eq!(result, Ok(4));
        assert_eq!(allocations, 0);
    }
}
