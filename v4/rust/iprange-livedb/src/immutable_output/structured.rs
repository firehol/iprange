//! Structured-value construction for immutable outputs.

use crate::contract::{AddressFamily, StructureKind, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::membership_delta::Delta;
use crate::structured_value::{self, NetworkEnrichmentV1, NetworkEnrichmentV1Codec};

use super::{ranges, reference_batch, Builder, MembershipWords};

impl Builder {
    pub(crate) fn push_network_enrichment_v1_v4<W: MembershipWords>(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        self.mutate(|output| {
            output.push_network_enrichment_v1_v4_inner(from, to, value, membership)
        })
    }

    pub(crate) fn push_network_enrichment_v1_v6<W: MembershipWords>(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        self.mutate(|output| {
            output.push_network_enrichment_v1_v6_inner(from, to, value, membership)
        })
    }

    fn push_network_enrichment_v1_v4_inner<W: MembershipWords>(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        self.require_structure_mode(StructureKind::NetworkEnrichmentV1, AddressFamily::Ipv4)?;
        let structure = self.intern_network_enrichment_v1(value, membership)?;
        self.ranges.push_v4(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record {
                from,
                to,
                value: structure,
            },
        )?;
        self.add_structure_reference(structure)
    }

    fn push_network_enrichment_v1_v6_inner<W: MembershipWords>(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        self.require_structure_mode(StructureKind::NetworkEnrichmentV1, AddressFamily::Ipv6)?;
        let structure = self.intern_network_enrichment_v1(value, membership)?;
        self.ranges.push_v6(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record {
                from,
                to,
                value: structure,
            },
        )?;
        self.add_structure_reference(structure)
    }

    fn intern_network_enrichment_v1<W: MembershipWords>(
        &mut self,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<u32> {
        let membership_id = membership
            .map(|words| self.intern_membership(words))
            .transpose()?
            .unwrap_or(0);
        let payload = NetworkEnrichmentV1Codec::encode(value, membership_id)?;
        let mut state = structured_value::State::from_meta(&self.meta);
        let interned =
            structured_value::intern::<NetworkEnrichmentV1Codec, _>(self, &mut state, payload)?;
        state.write_to(&mut self.meta);
        if interned.id == 0 {
            return Err(Error::InvalidArgument(
                "an absent structure cannot create a range",
            ));
        }
        if interned.created && interned.membership_id != 0 {
            self.add_membership_reference(interned.membership_id)?;
        }
        Ok(interned.id)
    }

    fn add_structure_reference(&mut self, value: u32) -> Result<()> {
        match self.structure_references.add(value)? {
            reference_batch::Add::Added => return Ok(()),
            reference_batch::Add::Direct => return self.apply_structure_reference(value),
            reference_batch::Add::Full => {}
        }
        self.flush_structure_references()?;
        match self.structure_references.add(value)? {
            reference_batch::Add::Added => Ok(()),
            reference_batch::Add::Full => Err(Error::Corrupt(
                "empty structure reference batch stayed full",
            )),
            reference_batch::Add::Direct => self.apply_structure_reference(value),
        }
    }

    fn apply_structure_reference(&mut self, value: u32) -> Result<()> {
        let mut state = structured_value::State::from_meta(&self.meta);
        structured_value::apply_delta::<NetworkEnrichmentV1Codec, _>(
            self,
            &mut state,
            Delta {
                id: value,
                change: 1,
            },
        )?;
        state.write_to(&mut self.meta);
        Ok(())
    }

    pub(super) fn flush_structure_references(&mut self) -> Result<()> {
        if self.structure_references.is_empty() {
            return Ok(());
        }
        let mut state = structured_value::State::from_meta(&self.meta);
        for index in 0..self.structure_references.len() {
            if let Some(delta) = self.structure_references.take(index) {
                structured_value::apply_delta::<NetworkEnrichmentV1Codec, _>(
                    self, &mut state, delta,
                )?;
            }
        }
        self.structure_references.finish_flush();
        state.write_to(&mut self.meta);
        Ok(())
    }

    fn require_structure_mode(&self, kind: StructureKind, family: AddressFamily) -> Result<()> {
        if self.meta.value_kind != ValueKind::Structured || self.meta.structure_kind() != Some(kind)
        {
            return Err(Error::WrongStructureKind(
                "immutable output operation does not match its structure kind",
            ));
        }
        if self.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "immutable output operation does not match its address family",
            ));
        }
        Ok(())
    }
}
