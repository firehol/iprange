use crate::contract::{u32_le, u64_le, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;

use super::{
    coverage, require_query_header, required_level, Kind, BRANCH_CHILDREN, HEADER_SIZE, LEAF_BITS,
};
use crate::validation::context::Context;
use crate::validation::ValidationSink;

pub(crate) struct WordCache {
    root: u32,
    limit: u64,
    kind: Kind,
    leaf_base: Option<u64>,
    present: bool,
    page: [u8; PAGE_SIZE],
}

impl WordCache {
    pub(crate) fn new(root: u32, limit: u64, kind: Kind) -> Self {
        Self {
            root,
            limit,
            kind,
            leaf_base: None,
            present: false,
            page: [0; PAGE_SIZE],
        }
    }

    pub(crate) fn word<S: ValidationSink>(
        &mut self,
        context: &Context<'_, S>,
        word_index: u32,
    ) -> Result<u64> {
        let bit = u64::from(word_index)
            .checked_mul(64)
            .ok_or(Error::ArithmeticOverflow("validation bitmap word"))?;
        if bit >= self.limit || self.root == 0 {
            return Ok(0);
        }
        let leaf_base = bit / LEAF_BITS * LEAF_BITS;
        if self.leaf_base != Some(leaf_base) {
            self.load_leaf(context, bit, leaf_base)?;
        }
        if !self.present {
            return Ok(0);
        }
        let local_word = usize::try_from((bit - leaf_base) / 64)
            .map_err(|_| Error::ArithmeticOverflow("validation bitmap word"))?;
        Ok(u64_le(&self.page, HEADER_SIZE + local_word * 8))
    }

    fn load_leaf<S: ValidationSink>(
        &mut self,
        context: &Context<'_, S>,
        bit: u64,
        leaf_base: u64,
    ) -> Result<()> {
        let mut page_number = self.root;
        let mut level = required_level(self.limit)?;
        let mut base = 0u64;
        loop {
            self.read_page(context, page_number, level)?;
            if level == 0 {
                self.leaf_base = Some(base);
                self.present = true;
                return Ok(());
            }
            let (child, child_base) = self.selected_child(bit, base, level)?;
            if child == 0 {
                self.leaf_base = Some(leaf_base);
                self.present = false;
                return Ok(());
            }
            base = child_base;
            page_number = child;
            level -= 1;
        }
    }

    fn read_page<S: ValidationSink>(
        &mut self,
        context: &Context<'_, S>,
        page_number: u32,
        level: u16,
    ) -> Result<()> {
        file_io::read_page(
            context.file,
            page_number,
            context.meta.page_count,
            &mut self.page,
        )?;
        require_query_header(&self.page, context.meta.txn_id, self.kind, level)
    }

    fn selected_child(&self, bit: u64, base: u64, level: u16) -> Result<(u32, u64)> {
        let span = coverage(level - 1)?;
        let index = usize::try_from((bit - base) / span)
            .map_err(|_| Error::ArithmeticOverflow("validation bitmap word"))?;
        if index >= BRANCH_CHILDREN {
            return Err(Error::Corrupt(
                "validated bitmap child is outside its branch",
            ));
        }
        let child = u32_le(&self.page, HEADER_SIZE + 32 + index * 4);
        let child_base = base
            .checked_add(
                span.checked_mul(index as u64)
                    .ok_or(Error::ArithmeticOverflow("validation bitmap word"))?,
            )
            .ok_or(Error::ArithmeticOverflow("validation bitmap word"))?;
        Ok((child, child_base))
    }
}
