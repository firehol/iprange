//! Test-only accounting for necessary low-level work.

#[cfg(test)]
use std::cell::RefCell;

#[cfg(test)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct Snapshot {
    pub(crate) tree_lookups: u64,
    pub(crate) tree_descents: u64,
    pub(crate) pages_visited: u64,
    pub(crate) pages_created: u64,
    pub(crate) pages_copied: u64,
    pub(crate) pages_split: u64,
    pub(crate) pages_merged: u64,
    pub(crate) pages_retired: u64,
    pub(crate) pages_reclaimed: u64,
    pub(crate) pages_sealed: u64,
    pub(crate) ranges_consumed: u64,
    pub(crate) ranges_emitted: u64,
    pub(crate) ranges_split: u64,
    pub(crate) ranges_coalesced: u64,
    pub(crate) catalog_lookups: u64,
    pub(crate) catalog_interns: u64,
    pub(crate) membership_lookups: u64,
    pub(crate) membership_interns: u64,
    pub(crate) mapping_growths: u64,
    pub(crate) mapping_remaps: u64,
    pub(crate) mapping_flushes: u64,
    pub(crate) file_syncs: u64,
    pub(crate) source_passes: u64,
    pub(crate) output_passes: u64,
}

#[cfg(test)]
thread_local! {
    static CURRENT: RefCell<Snapshot> = RefCell::new(Snapshot::default());
}

#[cfg(test)]
fn update(change: impl FnOnce(&mut Snapshot)) {
    CURRENT.with_borrow_mut(change);
}

macro_rules! event {
    ($function:ident, $field:ident) => {
        #[inline(always)]
        pub(crate) fn $function(amount: u64) {
            #[cfg(test)]
            update(|current| current.$field += amount);
            #[cfg(not(test))]
            let _ = amount;
        }
    };
}

event!(tree_lookup, tree_lookups);
event!(tree_descent, tree_descents);
event!(page_visited, pages_visited);
event!(page_created, pages_created);
event!(page_copied, pages_copied);
event!(page_split, pages_split);
event!(page_retired, pages_retired);
event!(page_reclaimed, pages_reclaimed);
event!(page_sealed, pages_sealed);
event!(range_consumed, ranges_consumed);
event!(range_emitted, ranges_emitted);
event!(range_split, ranges_split);
event!(range_coalesced, ranges_coalesced);
event!(catalog_lookup, catalog_lookups);
event!(catalog_intern, catalog_interns);
event!(membership_lookup, membership_lookups);
event!(membership_intern, membership_interns);
event!(mapping_growth, mapping_growths);
event!(mapping_remap, mapping_remaps);
event!(mapping_flush, mapping_flushes);
event!(file_sync, file_syncs);
event!(source_pass, source_passes);
event!(output_pass, output_passes);

#[cfg(test)]
pub(crate) fn reset() {
    CURRENT.with_borrow_mut(|current| *current = Snapshot::default());
}

#[cfg(test)]
pub(crate) fn snapshot() -> Snapshot {
    CURRENT.with_borrow(|current| *current)
}

#[cfg(test)]
pub(crate) fn measure<T>(operation: impl FnOnce() -> T) -> (T, Snapshot) {
    reset();
    let result = operation();
    (result, snapshot())
}
