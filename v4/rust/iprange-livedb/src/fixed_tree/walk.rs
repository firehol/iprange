//! Fixed-depth postorder retirement of one detached tree.

use crate::contract::{MAX_TREE_LEVEL, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::slotted_page::Header;

use super::page::{branch_child, parse};
use super::{Codec, RetiringStore};

const MAX_DEPTH: usize = MAX_TREE_LEVEL as usize;

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    next_child: usize,
    child_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    next_child: 0,
    child_count: 0,
    level: 0,
};

struct Walker {
    stack: [Frame; MAX_DEPTH],
    depth: usize,
    current: u32,
    expected_level: Option<u16>,
}

pub(crate) fn retire_tree<C, S, F>(store: &mut S, root: u32, mut checkpoint: F) -> Result<()>
where
    C: Codec,
    S: RetiringStore,
    F: FnMut() -> Result<()>,
{
    if root == 0 {
        return Ok(());
    }
    Walker::new(root).run::<C, S, F>(store, &mut checkpoint)
}

impl Walker {
    fn run<C, S, F>(&mut self, store: &mut S, checkpoint: &mut F) -> Result<()>
    where
        C: Codec,
        S: RetiringStore,
        F: FnMut() -> Result<()>,
    {
        loop {
            checkpoint()?;
            if self.visit::<C, S>(store)? {
                return Ok(());
            }
        }
    }

    fn visit<C: Codec, S: RetiringStore>(&mut self, store: &mut S) -> Result<bool> {
        let (page, header) = read_current::<C, S>(store, self)?;
        if header.level > 0 {
            self.descend::<C, S>(store, &page, header)?;
            return Ok(false);
        }
        store.retire_pages(&[self.current])?;
        Ok(!self.advance::<C, S>(store)?)
    }

    fn new(root: u32) -> Self {
        Self {
            stack: [EMPTY_FRAME; MAX_DEPTH],
            depth: 0,
            current: root,
            expected_level: None,
        }
    }

    fn descend<C: Codec, S: RetiringStore>(
        &mut self,
        store: &S,
        page: &[u8; PAGE_SIZE],
        header: Header,
    ) -> Result<()> {
        let slot = self
            .stack
            .get_mut(self.depth)
            .ok_or(Error::Corrupt("B+tree exceeds its maximum height"))?;
        *slot = Frame {
            page_number: self.current,
            next_child: 1,
            child_count: header.item_count,
            level: header.level,
        };
        self.depth += 1;
        self.current = branch_child::<C>(page, &header, 0, store.page_limit())?;
        self.expected_level = Some(header.level - 1);
        Ok(())
    }

    fn advance<C: Codec, S: RetiringStore>(&mut self, store: &mut S) -> Result<bool> {
        loop {
            let Some(slot) = self.depth.checked_sub(1) else {
                return Ok(false);
            };
            let frame = self.stack[slot];
            if frame.next_child < frame.child_count {
                self.select_next_child::<C, S>(store, slot, frame)?;
                return Ok(true);
            }
            self.depth = slot;
            store.retire_pages(&[frame.page_number])?;
        }
    }

    fn select_next_child<C: Codec, S: RetiringStore>(
        &mut self,
        store: &S,
        slot: usize,
        mut frame: Frame,
    ) -> Result<()> {
        let mut parent = [0; PAGE_SIZE];
        store.read(frame.page_number, &mut parent)?;
        let header = parse::<C>(&parent, store.target_txn(), Some(frame.level))?;
        if header.item_count != frame.child_count {
            return Err(Error::Corrupt("B+tree changed during retirement"));
        }
        self.current = branch_child::<C>(&parent, &header, frame.next_child, store.page_limit())?;
        frame.next_child += 1;
        self.stack[slot] = frame;
        self.expected_level = Some(frame.level - 1);
        Ok(())
    }
}

fn read_current<C: Codec, S: RetiringStore>(
    store: &S,
    walker: &Walker,
) -> Result<([u8; PAGE_SIZE], Header)> {
    let mut page = [0; PAGE_SIZE];
    store.read(walker.current, &mut page)?;
    let header = parse::<C>(&page, store.target_txn(), walker.expected_level)?;
    Ok((page, header))
}
