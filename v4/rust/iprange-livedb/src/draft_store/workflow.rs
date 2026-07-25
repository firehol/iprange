//! Exact direct-workflow preparation in unpublished pages.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::contract::AddressFamily;
use crate::error::Result;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::{Cursor, RangeDirection};
use crate::range_mutation;

use super::DraftStore;

impl DraftStore<'_> {
    pub(crate) fn preserve_retention_values(
        &mut self,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        match base.meta.address_family {
            AddressFamily::Ipv4 => self.preserve::<Ipv4Key>(base, cancellation),
            AddressFamily::Ipv6 => self.preserve::<Ipv6Key>(base, cancellation),
        }
    }

    pub(crate) fn finish_direct_workflow(
        &mut self,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cancellation.check()?;
        match base.meta.address_family {
            AddressFamily::Ipv4 => {
                range_mutation::retire_tree::<Ipv4Key, _, _>(self, base.meta.range_root, || {
                    cancellation.check()
                })?;
            }
            AddressFamily::Ipv6 => {
                range_mutation::retire_tree::<Ipv6Key, _, _>(self, base.meta.range_root, || {
                    cancellation.check()
                })?;
            }
        }
        self.draft.finish_workflow();
        Ok(())
    }

    fn preserve<K: crate::key::IpKey>(
        &mut self,
        base: &Bootstrap,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let file = self.file;
        let mut cursor = Cursor::<K>::new(file, &base.meta, RangeDirection::Forward, None)?;
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        while let Some(old) = cursor.next()? {
            cancellation.check()?;
            range_mutation::transform(
                self,
                &mut root,
                &mut count,
                old.from,
                old.to,
                |_, current| {
                    cancellation.check()?;
                    Ok(current.map(|_| old.value))
                },
            )?;
        }
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        Ok(())
    }
}
