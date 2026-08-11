//! Hardcoded typed values over one common mapped structure manager.

pub(crate) mod codec;
mod cursor;
mod manager;
mod network_enrichment_v1;
pub(crate) mod table;
mod view;

pub(crate) use codec::{Payload, PayloadCodec};
pub use cursor::{
    NetworkEnrichmentV1CursorV4, NetworkEnrichmentV1CursorV6, NetworkEnrichmentV1Range,
};
pub(crate) use manager::{apply_delta, find, intern, payload_digest, State};
pub(crate) use network_enrichment_v1::Codec as NetworkEnrichmentV1Codec;
pub use network_enrichment_v1::{NetworkEnrichmentV1, NetworkEnrichmentV1Location};
pub use view::NetworkEnrichmentV1View;

pub(crate) use view::{by_id, lookup_v4, lookup_v6, membership_id, require_kind};
