//! Bounded resolution of caller names into global algebra positions.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};

use super::{AlgebraAccess, FeedSelection};
use crate::heap::Heap;

pub(super) enum Selection {
    All { count: usize },
    Named { positions: Vec<u32>, flags: Vec<u8> },
}

impl Selection {
    pub(super) fn resolve(
        algebra: &(impl AlgebraAccess + ?Sized),
        requested: FeedSelection<'_>,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        match requested {
            FeedSelection::All => Ok(Self::All {
                count: algebra.state().names().len(),
            }),
            FeedSelection::Named(names) => {
                if names.is_empty() {
                    return Err(Error::InvalidArgument(
                        "membership algebra feed selection is empty",
                    ));
                }
                let mut positions =
                    heap.vector(names.len(), "membership algebra selection heap")?;
                for (work, name) in names.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    let position = algebra
                        .state()
                        .names()
                        .binary_search(name)
                        .map_err(|_| Error::NameNotFound)?;
                    positions.push(
                        u32::try_from(position)
                            .map_err(|_| Error::BudgetExceeded("membership algebra feeds"))?,
                    );
                }
                cancellation.check()?;
                positions.sort_unstable();
                cancellation.check()?;
                for (work, pair) in positions.windows(2).enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    if pair[0] == pair[1] {
                        return Err(Error::InvalidArgument(
                            "membership algebra feed selection is not unique",
                        ));
                    }
                }
                let mut flags = heap.filled(
                    algebra.state().names().len(),
                    0u8,
                    "membership algebra selection heap",
                )?;
                for (work, &position) in positions.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    flags[position as usize] = 1;
                }
                Ok(Self::Named { positions, flags })
            }
        }
    }

    pub(super) fn len(&self) -> usize {
        match self {
            Self::All { count } => *count,
            Self::Named { positions, .. } => positions.len(),
        }
    }

    pub(super) fn any(
        &self,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        match self {
            Self::All { .. } => Ok(!present.is_empty()),
            Self::Named { positions, .. } if positions.len() < present.len() => {
                contains_present(positions, counts, cancellation)
            }
            Self::Named { flags, .. } => contains_selected(present, flags, cancellation),
        }
    }

    pub(super) fn all(
        &self,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        match self {
            Self::All { count } => Ok(*count != 0 && present.len() == *count),
            Self::Named { positions, .. } => {
                if positions.len() > present.len() {
                    return Ok(false);
                }
                for (work, &position) in positions.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    if counts[position as usize] == 0 {
                        return Ok(false);
                    }
                }
                Ok(!positions.is_empty())
            }
        }
    }

    pub(super) fn for_each_position(
        &self,
        cancellation: &CancellationToken,
        mut apply: impl FnMut(u32) -> Result<()>,
    ) -> Result<()> {
        match self {
            Self::All { count } => {
                for position in 0..*count {
                    if position & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    apply(position as u32)?;
                }
            }
            Self::Named { positions, .. } => {
                for (work, &position) in positions.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    apply(position)?;
                }
            }
        }
        Ok(())
    }

    /// Visit present selected positions and report whether they were ascending.
    pub(super) fn for_each_present(
        &self,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
        mut apply: impl FnMut(u32) -> Result<()>,
    ) -> Result<bool> {
        match self {
            Self::All { .. } => {
                for (work, &position) in present.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    apply(position)?;
                }
                Ok(present.len() < 2)
            }
            Self::Named { positions, .. } if positions.len() <= present.len() => {
                for (work, &position) in positions.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    if counts[position as usize] != 0 {
                        apply(position)?;
                    }
                }
                Ok(true)
            }
            Self::Named { flags, .. } => {
                for (work, &position) in present.iter().enumerate() {
                    if work & 4095 == 4095 {
                        cancellation.check()?;
                    }
                    if flags[position as usize] != 0 {
                        apply(position)?;
                    }
                }
                Ok(present.len() < 2)
            }
        }
    }
}

fn contains_present(
    positions: &[u32],
    counts: &[u32],
    cancellation: &CancellationToken,
) -> Result<bool> {
    for (work, &position) in positions.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        if counts[position as usize] != 0 {
            return Ok(true);
        }
    }
    Ok(false)
}

fn contains_selected(
    present: &[u32],
    flags: &[u8],
    cancellation: &CancellationToken,
) -> Result<bool> {
    for (work, &position) in present.iter().enumerate() {
        if work & 4095 == 4095 {
            cancellation.check()?;
        }
        if flags[position as usize] != 0 {
            return Ok(true);
        }
    }
    Ok(false)
}
