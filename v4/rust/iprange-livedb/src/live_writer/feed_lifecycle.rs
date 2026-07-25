//! Immediate named-feed delete and rename workflows.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;

use super::{LiveWriter, PreparedFeedChange};

impl LiveWriter {
    /// Delete one existing feed while preserving every other feed.
    pub fn delete_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedFeedChange<'_>> {
        let feed = self.require_existing_feed(name, cancellation)?;
        self.start_feed_workflow_draft()?;
        let token = cancellation.clone();
        self.mutate(|store| {
            store.delete_feed_membership_cancellable(feed, &mut || token.check())?;
            store.finish_membership_workflow(&token)
        })?;
        Ok(PreparedFeedChange::new(self, cancellation.clone()))
    }

    /// Rename one existing feed without changing its index or membership.
    pub fn rename_feed(
        &mut self,
        old: FeedName,
        new: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedFeedChange<'_>> {
        let feed = self.require_existing_feed(old, cancellation)?;
        if feed_catalog::lookup(&self.file, &self.base.meta, &new)?.is_some() {
            return Err(Error::NameExists);
        }
        cancellation.check()?;
        self.start_feed_workflow_draft()?;
        let token = cancellation.clone();
        self.mutate(|store| {
            token.check()?;
            store.rename_feed_ref(feed, new)?;
            store.finish_membership_workflow(&token)
        })?;
        Ok(PreparedFeedChange::new(self, cancellation.clone()))
    }

    fn require_existing_feed(
        &self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<FeedEntry> {
        self.require_feed_workflow_ready()?;
        let feed =
            feed_catalog::lookup(&self.file, &self.base.meta, &name)?.ok_or(Error::NameNotFound)?;
        cancellation.check()?;
        Ok(feed)
    }
}
