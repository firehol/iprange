//! Private-tree gap insertion and bounded edge reuse.

use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::page_io::PageEdit;
use crate::slotted_page;

use super::insert::{
    apply_leaf_edit, edit_leaf, locate_private_position, new_leaf, propagate_first, replace_target,
    require_leaf, require_replacement, split_leaf_at_edge, LeafTarget,
};
use super::page::{self, lower_bound, parse, Edit};
use super::{private_path_select, Codec, LeafSelector, Path, PrivateLeaf, RetiredPages, Store};

// The selected path stays inline to avoid a heap allocation on rejected local
// inserts. General insertion has no positioned return payload.
#[allow(clippy::large_enum_variant)]
pub(crate) enum LocalInsert<R> {
    Inserted(LocalInserted),
    General(LocalReject<R>),
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct LocalInserted {
    page_number: u32,
}

impl LocalInserted {
    pub(crate) const fn page_number(self) -> u32 {
        self.page_number
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CachedInsert {
    Inserted,
    Miss,
}

pub(crate) enum EdgeInsert<K, R> {
    Inserted(PrivateEdge<K>),
    General(LocalReject<R>),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Edge {
    First,
    Last,
}

#[derive(Debug)]
pub(crate) struct PrivatePosition {
    pub(super) path: Path,
    pub(super) page_number: u32,
}

#[derive(Debug)]
pub(crate) struct PrivateEdge<K> {
    position: PrivatePosition,
    direction: Option<Edge>,
    pending_first: Option<K>,
}

impl<K> PrivateEdge<K> {
    pub(crate) fn consistent(position: PrivatePosition) -> Self {
        Self {
            position,
            direction: None,
            pending_first: None,
        }
    }
}

pub(crate) fn root_edge<K>(page_number: u32) -> PrivateEdge<K> {
    PrivateEdge::consistent(PrivatePosition {
        path: Path::new(),
        page_number,
    })
}

pub(crate) fn flush_edge<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    edge: &mut PrivateEdge<C::Key>,
) -> Result<()> {
    if let Some(key) = edge.pending_first {
        propagate_first::<C, S>(store, root, &edge.position.path, key)?;
        edge.pending_first = None;
    }
    Ok(())
}

pub(crate) struct LocalReject<R> {
    target: LeafTarget,
    predecessor: Option<(usize, R)>,
    successor: Option<(usize, R)>,
    predecessor_complete: bool,
    successor_complete: bool,
}

impl<R> LocalReject<R> {
    pub(crate) fn predecessor(&self) -> Option<&R> {
        self.predecessor
            .as_ref()
            .map(|(_, predecessor)| predecessor)
    }

    pub(crate) fn successor(&self) -> Option<&R> {
        self.successor.as_ref().map(|(_, successor)| successor)
    }

    pub(crate) const fn predecessor_complete(&self) -> bool {
        self.predecessor_complete
    }

    pub(crate) const fn successor_complete(&self) -> bool {
        self.successor_complete
    }

    pub(crate) fn external_predecessor<C: Codec, S: Store>(
        &self,
        store: &S,
    ) -> Result<Option<C::Leaf>> {
        super::read::adjacent_leaf::<C, S>(store, &self.target.path, super::read::Adjacent::Before)
            .map(|leaf| leaf.map(|(_, value)| value))
    }

    pub(crate) fn external_successor<C: Codec, S: Store>(
        &self,
        store: &S,
    ) -> Result<Option<C::Leaf>> {
        super::read::adjacent_leaf::<C, S>(store, &self.target.path, super::read::Adjacent::After)
            .map(|leaf| leaf.map(|(_, value)| value))
    }

    pub(crate) fn into_position(self) -> PrivatePosition {
        PrivatePosition {
            path: self.target.path,
            page_number: self.target.page_number,
        }
    }
}

pub(crate) trait LocalGap<C: Codec> {
    type Reject;

    fn previous<B: ByteSource>(
        &mut self,
        exact: bool,
        cell: Option<B>,
    ) -> Result<LocalPrevious<Self::Reject>>;
    fn next<B: ByteSource>(&mut self, cell: Option<B>) -> Result<LocalNext<Self::Reject>>;
}

pub(crate) enum LocalPrevious<R> {
    Accept,
    Reject(R),
}

pub(crate) enum LocalNext<R> {
    Accept,
    Reject(R),
}

pub(crate) fn insert_if_local_gap<C, S, G>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    retired: &mut RetiredPages,
    gap: &mut G,
) -> Result<LocalInsert<G::Reject>>
where
    C: Codec,
    S: Store,
    G: LocalGap<C>,
{
    require_leaf::<C>(leaf_cell)?;
    if *root == 0 {
        let page_number = new_leaf::<C, S>(store, leaf_cell)?;
        *root = page_number;
        return Ok(LocalInsert::Inserted(LocalInserted { page_number }));
    }

    let key = C::read_key(leaf_cell, 0)?;
    let mut selector = GapSelector::<C, G>::new(key, leaf_cell.len(), gap);
    let leaf = private_path_select::<C, S, _>(store, root, key, retired, &mut selector)?;
    if !retired.as_slice().is_empty() {
        return Err(Error::Corrupt("private B+tree contains a committed page"));
    }
    let PrivateLeaf {
        path,
        page_number,
        header,
        selection,
    } = leaf;
    let (index, fits) = match selection {
        GapDecision::Insert { index, fits } => (index, fits),
        decision => {
            return Ok(LocalInsert::General(rejection(
                path,
                page_number,
                header,
                decision,
            )?));
        }
    };
    let target = LeafTarget {
        path,
        page_number,
        header,
        index,
        exists: false,
    };
    let page_number = target.page_number;
    let positioned = insert_gap_target::<C, S>(store, root, leaf_cell, target, key, fits)?;
    Ok(LocalInsert::Inserted(LocalInserted {
        page_number: positioned.map_or(page_number, |position| position.page_number),
    }))
}

pub(crate) fn insert_if_cached_interior_gap<C, S, G>(
    store: &mut S,
    page_number: u32,
    leaf_cell: &[u8],
    gap: &mut G,
) -> Result<CachedInsert>
where
    C: Codec,
    S: Store,
    G: LocalGap<C>,
{
    require_leaf::<C>(leaf_cell)?;
    let key = C::read_key(leaf_cell, 0)?;
    let selected = store.inspect_page(page_number, |page| {
        let header = parse::<C, _>(page, store.target_txn(), Some(0))?;
        if crate::page_header::born_txn(page) != store.target_txn() {
            return Err(Error::Corrupt("leaf locator selected a committed page"));
        }
        let (index, exists) = lower_bound::<C, _>(page, &header, key, true)?;
        if exists || index == 0 || index == header.item_count {
            return Ok(None);
        }
        let mut selector = GapSelector::<C, G>::new(key, leaf_cell.len(), gap);
        match selector.select_at(page, &header, &Path::new(), index, false)? {
            GapDecision::Insert { fits: true, .. } => Ok(Some((header, index))),
            GapDecision::Insert { fits: false, .. } | GapDecision::General { .. } => Ok(None),
        }
    })?;
    let Some((header, index)) = selected else {
        return Ok(CachedInsert::Miss);
    };
    apply_leaf_edit::<C, S>(
        store,
        page_number,
        &header,
        Edit {
            index,
            replace: false,
            cell: leaf_cell,
        },
    )?;
    Ok(CachedInsert::Inserted)
}

pub(crate) fn insert_rejected_gap<C: Codec, S: Store, R>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    rejected: LocalReject<R>,
) -> Result<Option<PrivatePosition>> {
    require_leaf::<C>(leaf_cell)?;
    let key = C::read_key(leaf_cell, 0)?;
    let target = rejected.target;
    let fits = slotted_page::insert_fits(&target.header, leaf_cell.len());
    insert_gap_target::<C, S>(store, root, leaf_cell, target, key, fits)
}

fn insert_gap_target<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    target: LeafTarget,
    key: C::Key,
    fits: bool,
) -> Result<Option<PrivatePosition>> {
    if fits {
        apply_leaf_edit::<C, S>(
            store,
            target.page_number,
            &target.header,
            Edit {
                index: target.index,
                replace: false,
                cell: leaf_cell,
            },
        )?;
        if target.index == 0 {
            propagate_first::<C, S>(store, root, &target.path, key)?;
        }
        Ok(Some(PrivatePosition {
            path: target.path,
            page_number: target.page_number,
        }))
    } else {
        edit_leaf::<C, S>(store, root, leaf_cell, target)?;
        Ok(None)
    }
}

pub(crate) fn insert_if_edge_gap<C, S, G>(
    store: &mut S,
    root: &mut u32,
    leaf_cell: &[u8],
    mut cached: PrivateEdge<C::Key>,
    edge: Edge,
    known_gap: bool,
    gap: &mut G,
) -> Result<EdgeInsert<C::Key, G::Reject>>
where
    C: Codec,
    S: Store,
    G: LocalGap<C>,
{
    require_leaf::<C>(leaf_cell)?;
    if *root == 0 {
        return Err(Error::Corrupt("cached B+tree edge has an empty root"));
    }
    verify_cached_edge(&mut cached, *root, edge)?;
    let key = C::read_key(leaf_cell, 0)?;
    let (header, decision) = store.inspect_page(cached.position.page_number, |page| {
        let header = parse::<C, _>(page, store.target_txn(), Some(0))?;
        if crate::page_header::born_txn(page) != store.target_txn() {
            return Err(Error::Corrupt("cached B+tree edge is not private"));
        }
        let boundary = match edge {
            Edge::First => 0,
            Edge::Last => header.item_count - 1,
        };
        let boundary_key = page::key_at::<C, _>(page, &header, boundary)?;
        let (index, exists) = match edge {
            Edge::First if key < boundary_key => (0, false),
            Edge::First if key == boundary_key => (0, true),
            Edge::Last if key > boundary_key => (header.item_count, false),
            Edge::Last if key == boundary_key => (header.item_count - 1, true),
            _ => return Err(Error::Corrupt("cached B+tree edge order changed")),
        };
        let decision = if known_gap {
            if exists {
                return Err(Error::Corrupt("known B+tree edge gap contains its key"));
            }
            // The ordered-input owner retained the immediately preceding edge
            // interval and proved this interval is separate from it.
            GapDecision::Insert {
                index,
                fits: slotted_page::insert_fits(&header, leaf_cell.len()),
            }
        } else {
            let mut selector = GapSelector::<C, G>::new(key, leaf_cell.len(), gap);
            selector.select_at(page, &header, &cached.position.path, index, exists)?
        };
        Ok((header, decision))
    })?;
    let (index, fits) = match decision {
        GapDecision::Insert { index, fits } => (index, fits),
        decision => {
            flush_edge::<C, S>(store, root, &mut cached)?;
            return Ok(EdgeInsert::General(rejection(
                cached.position.path,
                cached.position.page_number,
                header,
                decision,
            )?));
        }
    };
    let target = LeafTarget {
        path: cached.position.path,
        page_number: cached.position.page_number,
        header,
        index,
        exists: false,
    };
    if fits {
        let pending_first = (target.index == 0).then_some((target.path.depth != 0).then_some(key));
        cached.position = apply_fitting_edge_insert::<C, S>(store, target, leaf_cell)?;
        if let Some(pending_first) = pending_first {
            cached.pending_first = pending_first;
        }
    } else {
        split_leaf_at_edge::<C, S>(store, root, target, leaf_cell, edge)?;
        cached.pending_first = None;
        cached.position = locate_private_position::<C, S>(store, root, key)?;
    }
    Ok(EdgeInsert::Inserted(cached))
}

#[inline(always)]
fn verify_cached_edge<K>(cached: &mut PrivateEdge<K>, root: u32, edge: Edge) -> Result<()> {
    match cached.direction {
        Some(direction) if direction != edge => {
            Err(Error::Corrupt("cached B+tree edge direction changed"))
        }
        Some(_) => Ok(()),
        None => {
            crate::work::edge_path_check(1);
            if !path_is_edge(&cached.position.path, edge)
                || (cached.position.path.depth == 0 && cached.position.page_number != root)
            {
                return Err(Error::Corrupt(
                    "cached B+tree position is not its claimed edge",
                ));
            }
            cached.direction = Some(edge);
            Ok(())
        }
    }
}

#[inline(always)]
fn apply_fitting_edge_insert<C: Codec, S: Store>(
    store: &mut S,
    target: LeafTarget,
    leaf_cell: &[u8],
) -> Result<PrivatePosition> {
    apply_leaf_edit::<C, S>(
        store,
        target.page_number,
        &target.header,
        Edit {
            index: target.index,
            replace: false,
            cell: leaf_cell,
        },
    )?;
    Ok(PrivatePosition {
        path: target.path,
        page_number: target.page_number,
    })
}

fn rejection<R>(
    path: Path,
    page_number: u32,
    header: crate::slotted_page::Header,
    decision: GapDecision<R>,
) -> Result<LocalReject<R>> {
    let GapDecision::General {
        index,
        predecessor,
        successor,
        predecessor_complete,
        successor_complete,
    } = decision
    else {
        return Err(Error::Corrupt("accepted B+tree gap became a rejection"));
    };
    Ok(LocalReject {
        target: LeafTarget {
            path,
            page_number,
            header,
            index,
            exists: false,
        },
        predecessor,
        successor,
        predecessor_complete,
        successor_complete,
    })
}

enum GapDecision<R> {
    General {
        index: usize,
        predecessor: Option<(usize, R)>,
        successor: Option<(usize, R)>,
        predecessor_complete: bool,
        successor_complete: bool,
    },
    Insert {
        index: usize,
        fits: bool,
    },
}

struct GapSelector<'a, C: Codec, G> {
    key: C::Key,
    cell_len: usize,
    gap: &'a mut G,
    marker: std::marker::PhantomData<C>,
}

impl<'a, C: Codec, G> GapSelector<'a, C, G> {
    fn new(key: C::Key, cell_len: usize, gap: &'a mut G) -> Self {
        Self {
            key,
            cell_len,
            gap,
            marker: std::marker::PhantomData,
        }
    }
}

impl<C, S, G> LeafSelector<C, S> for GapSelector<'_, C, G>
where
    C: Codec,
    S: Store,
    G: LocalGap<C>,
{
    type Selection = GapDecision<G::Reject>;

    fn select<'a>(
        &mut self,
        page: S::ReadPage<'a>,
        header: &crate::slotted_page::Header,
        path: &Path,
    ) -> Result<Self::Selection>
    where
        S: 'a,
    {
        let (index, exists) = lower_bound::<C, _>(page, header, self.key, true)?;
        self.select_at(page, header, path, index, exists)
    }
}

impl<C, G> GapSelector<'_, C, G>
where
    C: Codec,
    G: LocalGap<C>,
{
    fn select_at<S: ByteSource>(
        &mut self,
        page: S,
        header: &crate::slotted_page::Header,
        path: &Path,
        index: usize,
        exists: bool,
    ) -> Result<GapDecision<G::Reject>> {
        let (predecessor, predecessor_complete) = if exists {
            let cell = validated_leaf::<C, _>(page, header, index)?;
            match self.gap.previous(true, Some(cell))? {
                LocalPrevious::Reject(predecessor) => (Some((index, predecessor)), true),
                LocalPrevious::Accept => {
                    return Err(Error::Corrupt("exact B+tree key was accepted as a gap"));
                }
            }
        } else if index > 0 {
            let cell = validated_leaf::<C, _>(page, header, index - 1)?;
            let predecessor = match self.gap.previous(false, Some(cell))? {
                LocalPrevious::Accept => None,
                LocalPrevious::Reject(predecessor) => Some((index - 1, predecessor)),
            };
            (predecessor, true)
        } else if path.as_slice().iter().all(|frame| frame.index == 0) {
            match self.gap.previous::<S>(false, None)? {
                LocalPrevious::Accept => (None, true),
                LocalPrevious::Reject(_) => {
                    return Err(Error::Corrupt("absent B+tree predecessor was rejected"));
                }
            }
        } else {
            (None, false)
        };

        let successor_index = index + usize::from(exists);
        let (successor, successor_complete) = if successor_index < header.item_count {
            let cell = validated_leaf::<C, _>(page, header, successor_index)?;
            let successor = match self.gap.next(Some(cell))? {
                LocalNext::Accept => None,
                LocalNext::Reject(successor) => Some((successor_index, successor)),
            };
            (successor, true)
        } else if path
            .as_slice()
            .iter()
            .all(|frame| frame.index + 1 == frame.item_count)
        {
            match self.gap.next::<S>(None)? {
                LocalNext::Accept => (None, true),
                LocalNext::Reject(_) => {
                    return Err(Error::Corrupt("absent B+tree successor was rejected"));
                }
            }
        } else {
            (None, false)
        };

        if predecessor.is_some()
            || successor.is_some()
            || !predecessor_complete
            || !successor_complete
        {
            return Ok(GapDecision::General {
                index,
                predecessor,
                successor,
                predecessor_complete,
                successor_complete,
            });
        }
        Ok(GapDecision::Insert {
            index,
            fits: slotted_page::insert_fits(header, self.cell_len),
        })
    }
}

#[derive(Clone, Copy)]
pub(crate) enum LocalRun {
    Predecessor,
    Successor,
    Both,
}

pub(crate) fn replace_local_predecessor_with<C: Codec, S: Store, R>(
    store: &mut S,
    root: &mut u32,
    rejected: LocalReject<R>,
    key: C::Key,
    cells: &[&[u8]],
) -> Result<()> {
    require_replacement::<C>(key, cells)?;
    let (index, _) = rejected
        .predecessor
        .ok_or_else(|| Error::corrupt("B+tree local predecessor is unavailable"))?;
    let mut target = rejected.target;
    target.index = index;
    target.exists = true;
    replace_target::<C, S>(store, root, target, cells)
}

pub(crate) fn replace_local_run<C: Codec, S: Store, R>(
    store: &mut S,
    root: &mut u32,
    rejected: &LocalReject<R>,
    run: LocalRun,
    replacement: &[u8],
) -> Result<()> {
    require_leaf::<C>(replacement)?;
    let (start, remove_count) = match run {
        LocalRun::Predecessor => (
            rejected
                .predecessor
                .as_ref()
                .ok_or_else(|| Error::corrupt("local predecessor is unavailable"))?
                .0,
            1,
        ),
        LocalRun::Successor => (
            rejected
                .successor
                .as_ref()
                .ok_or_else(|| Error::corrupt("local successor is unavailable"))?
                .0,
            1,
        ),
        LocalRun::Both => {
            let predecessor = rejected
                .predecessor
                .as_ref()
                .ok_or_else(|| Error::corrupt("local predecessor is unavailable"))?
                .0;
            let successor = rejected
                .successor
                .as_ref()
                .ok_or_else(|| Error::corrupt("local successor is unavailable"))?
                .0;
            if successor != predecessor + 1 {
                return Err(Error::Corrupt("local B+tree run is not contiguous"));
            }
            (predecessor, 2)
        }
    };
    let target = &rejected.target;
    if start + remove_count > target.header.item_count {
        return Err(Error::Corrupt("local B+tree run is outside its leaf"));
    }
    let replacement_key = C::read_key(replacement, 0)?;
    store.update_page(target.page_number, |page| {
        let old_len = page::codec_cell::<C, _>(page.view(), &target.header, start)?.len();
        if replacement.len() != old_len {
            return Err(Error::Unsupported(
                "local B+tree run replacement changed cell size",
            ));
        }
        if start > 0
            && page::key_at::<C, _>(page.view(), &target.header, start - 1)? >= replacement_key
        {
            return Err(Error::Corrupt("local B+tree replacement is out of order"));
        }
        if start + remove_count < target.header.item_count
            && page::key_at::<C, _>(page.view(), &target.header, start + remove_count)?
                <= replacement_key
        {
            return Err(Error::Corrupt("local B+tree replacement is out of order"));
        }
        if !slotted_page::replace(page, &target.header, start, old_len, replacement)? {
            return Err(Error::Corrupt("local B+tree replacement no longer fits"));
        }
        let mut header = target.header;
        for _ in 1..remove_count {
            let old_len = page::codec_cell::<C, _>(page.view(), &header, start + 1)?.len();
            slotted_page::remove(page, &header, start + 1, old_len)?;
            header.item_count -= 1;
            header.lower -= 2;
            header.upper += old_len;
        }
        Ok(())
    })?;
    if start == 0 {
        propagate_first::<C, S>(store, root, &target.path, replacement_key)?;
    }
    Ok(())
}

fn path_is_edge(path: &Path, edge: Edge) -> bool {
    path.as_slice().iter().all(|frame| match edge {
        Edge::First => frame.index == 0,
        Edge::Last => frame.index + 1 == frame.item_count,
    })
}

fn validated_leaf<C: Codec, S: ByteSource>(
    page: S,
    header: &crate::slotted_page::Header,
    index: usize,
) -> Result<crate::mapping::ByteRange<S>> {
    let cell = page::codec_cell::<C, _>(page, header, index)?;
    C::validate_leaf(cell)?;
    Ok(cell)
}
