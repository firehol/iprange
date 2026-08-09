use crate::bitmap_page;
use crate::error::{Error, Result};

use super::{coverage, require_query_header, required_level, Kind};
use crate::validation::context::Context;
use crate::validation::ValidationSink;

pub(crate) struct WordCache {
    root: u32,
    limit: u64,
    kind: Kind,
    leaf_base: Option<u64>,
    leaf_page: Option<u32>,
    present: bool,
}

impl WordCache {
    pub(crate) fn new(root: u32, limit: u64, kind: Kind) -> Self {
        Self {
            root,
            limit,
            kind,
            leaf_base: None,
            leaf_page: None,
            present: false,
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
        let leaf_base = bit / bitmap_page::LEAF_BITS * bitmap_page::LEAF_BITS;
        if self.leaf_base != Some(leaf_base) {
            self.load_leaf(context, bit, leaf_base)?;
        }
        if !self.present {
            return Ok(0);
        }
        let local_word = usize::try_from((bit - leaf_base) / 64)
            .map_err(|_| Error::ArithmeticOverflow("validation bitmap word"))?;
        let page_number = self
            .leaf_page
            .ok_or(Error::Corrupt("validation bitmap leaf is missing"))?;
        let page = context.mapping.page(page_number, context.meta.page_count)?;
        require_query_header(page, context.meta.txn_id, self.kind, 0)?;
        bitmap_page::leaf_word(page, local_word)
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
            let page = context.mapping.page(page_number, context.meta.page_count)?;
            require_query_header(page, context.meta.txn_id, self.kind, level)?;
            if level == 0 {
                self.leaf_base = Some(base);
                self.leaf_page = Some(page_number);
                self.present = true;
                return Ok(());
            }
            let (child, child_base) = self.selected_child(page, bit, base, level)?;
            if child == 0 {
                self.leaf_base = Some(leaf_base);
                self.leaf_page = None;
                self.present = false;
                return Ok(());
            }
            base = child_base;
            page_number = child;
            level -= 1;
        }
    }

    fn selected_child<P: crate::mapping::ByteSource>(
        &self,
        page: P,
        bit: u64,
        base: u64,
        level: u16,
    ) -> Result<(u32, u64)> {
        let span = coverage(level - 1)?;
        let index = usize::try_from((bit - base) / span)
            .map_err(|_| Error::ArithmeticOverflow("validation bitmap word"))?;
        if index >= bitmap_page::BRANCH_CHILDREN {
            return Err(Error::Corrupt(
                "validated bitmap child is outside its branch",
            ));
        }
        let child = bitmap_page::branch_child(page, index)?;
        let child_base = base
            .checked_add(
                span.checked_mul(index as u64)
                    .ok_or(Error::ArithmeticOverflow("validation bitmap word"))?,
            )
            .ok_or(Error::ArithmeticOverflow("validation bitmap word"))?;
        Ok((child, child_base))
    }
}
