//! Immediate named-feed delete and rename workflows.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};

use super::{LiveWriter, PreparedFeedChange, PreparedState};

impl LiveWriter {
    /// Delete one existing feed while preserving every other feed.
    pub fn delete_feed(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedFeedChange<'_>> {
        let state = self.delete_feed_state(name, cancellation)?;
        Ok(PreparedFeedChange::from_state(self, state))
    }

    pub(crate) fn delete_feed_state(
        &mut self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedState> {
        let feed = self.require_existing_feed(name, cancellation)?;
        self.start_feed_workflow_draft()?;
        let token = cancellation.clone();
        self.mutate(|store| {
            store.delete_current_feed_membership_cancellable(feed, &mut || token.check())?;
            store.finish_membership_workflow(&token)
        })?;
        Ok(PreparedState::new(cancellation.clone()))
    }

    /// Rename one existing feed without changing its index or membership.
    pub fn rename_feed(
        &mut self,
        old: FeedName,
        new: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedFeedChange<'_>> {
        let state = self.rename_feed_state(old, new, cancellation)?;
        Ok(PreparedFeedChange::from_state(self, state))
    }

    pub(crate) fn rename_feed_state(
        &mut self,
        old: FeedName,
        new: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<PreparedState> {
        let feed = self.require_existing_feed(old, cancellation)?;
        if self.core.lookup_base_feed(&new)?.is_some() {
            return Err(Error::NameExists);
        }
        cancellation.check()?;
        self.start_feed_workflow_draft()?;
        let token = cancellation.clone();
        self.mutate(|store| {
            token.check()?;
            store.rename_current_feed_known_available(feed, new)?;
            store.finish_membership_workflow(&token)
        })?;
        Ok(PreparedState::new(cancellation.clone()))
    }

    fn require_existing_feed(
        &self,
        name: FeedName,
        cancellation: &CancellationToken,
    ) -> Result<FeedEntry> {
        self.require_feed_workflow_ready()?;
        let feed = self
            .core
            .lookup_base_feed(&name)?
            .ok_or(Error::NameNotFound)?;
        cancellation.check()?;
        Ok(feed)
    }
}
