//! Exact workflow preparation in unpublished pages.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::contract::AddressFamily;
use crate::error::Result;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_mutation;

use super::DraftStore;

impl DraftStore<'_> {
    pub(crate) fn finish_direct_workflow(
        &mut self,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        if !self.draft.base_range_tree_retired {
            match base.meta.address_family {
                AddressFamily::Ipv4 => {
                    range_mutation::retire_tree::<Ipv4Key, _, _>(
                        self,
                        base.meta.range_root,
                        || cancellation.check(),
                    )?;
                }
                AddressFamily::Ipv6 => {
                    range_mutation::retire_tree::<Ipv6Key, _, _>(
                        self,
                        base.meta.range_root,
                        || cancellation.check(),
                    )?;
                }
            }
        }
        self.draft.finish_workflow();
        Ok(())
    }

    pub(crate) fn finalize_membership_workflow(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        self.finish_membership_deltas_with_checkpoint(&mut || cancellation.check())?;
        cancellation.check()
    }

    pub(crate) fn finish_membership_workflow(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.finalize_membership_workflow(cancellation)?;
        self.draft.finish_workflow();
        Ok(())
    }
}
