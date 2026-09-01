//! The fixed v1 method registry and handler resolution (SOW-0028).
//!
//! Handlers receive the validated `Value` params and return either a
//! result object (the complete mechanical conversion) or a
//! HandlerError. The registry is the single authority for method
//! names here; unknown methods produce -32601 by the session.

use serde_json::Value;

/// Product-domain failure with the documented stable error data.
#[derive(Debug, Clone)]
pub struct HandlerError {
    pub code: &'static str,
    pub outcome: &'static str,
    pub message: String,
    pub details: Option<Value>,
}

impl HandlerError {
    pub fn new(code: &'static str, outcome: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            outcome,
            message: message.into(),
            details: None,
        }
    }
    pub fn invalid_params(message: impl Into<String>) -> Self {
        Self::new("invalid_argument", "not_started", message)
    }
}

/// A request handler: params in, complete converted result out.
pub type Handler =
    fn(state: &mut super::session::SessionState, params: Value) -> Result<Value, HandlerError>;

/// Strict per-method params schema. Err(message) becomes -32602.
pub type ParamsValidator = fn(&Value) -> Result<(), String>;

/// Method registry. Kept as a sorted static slice so describe can list
/// methods in bytewise order.
pub const METHODS: &[&str] = &[
    "iprange.v1.algebra.compare",
    "iprange.v1.algebra.count",
    "iprange.v1.algebra.publish",
    "iprange.v1.cancel",
    "iprange.v1.commit.resolve",
    "iprange.v1.current.publish",
    "iprange.v1.database.create",
    "iprange.v1.database.create.resolve",
    "iprange.v1.database.info",
    "iprange.v1.database.initialize_live",
    "iprange.v1.database.live_residue.resolve",
    "iprange.v1.database.live_transition.resolve",
    "iprange.v1.database.metadata.get",
    "iprange.v1.database.metadata.replace",
    "iprange.v1.database.reclaim",
    "iprange.v1.database.reset_live",
    "iprange.v1.direct.replace",
    "iprange.v1.export",
    "iprange.v1.feeds.create",
    "iprange.v1.feeds.delete",
    "iprange.v1.feeds.import",
    "iprange.v1.feeds.rename",
    "iprange.v1.feeds.replace",
    "iprange.v1.history.project",
    "iprange.v1.join.direct",
    "iprange.v1.join.membership",
    "iprange.v1.maintenance.list",
    "iprange.v1.maintenance.remove",
    "iprange.v1.publication.inspect",
    "iprange.v1.publication.residue.remove",
    "iprange.v1.publication.resolve",
    "iprange.v1.query.cardinalities",
    "iprange.v1.query.matching_feeds",
    "iprange.v1.query.overlaps",
    "iprange.v1.reader.close",
    "iprange.v1.reader.feeds.close",
    "iprange.v1.reader.feeds.next",
    "iprange.v1.reader.feeds.open",
    "iprange.v1.reader.info",
    "iprange.v1.reader.lookup",
    "iprange.v1.reader.matching_feeds",
    "iprange.v1.reader.metadata",
    "iprange.v1.reader.open",
    "iprange.v1.reader.ranges.close",
    "iprange.v1.reader.ranges.next",
    "iprange.v1.reader.ranges.open",
    "iprange.v1.recover",
    "iprange.v1.recovery.inspect",
    "iprange.v1.retention.first_seen.refresh",
    "iprange.v1.retention.last_seen.refresh",
    "iprange.v1.snapshot",
    "iprange.v1.system.describe",
    "iprange.v1.validate",
];

/// Methods advertised by system.describe: exactly the methods that are
/// callable in this build (the cancel notification is excluded), in
/// bytewise order. Advertisement is the capability handshake the
/// external runner uses to skip cases whose families are not shipped
/// yet; it must never list a method that would reply -32601.
pub fn advertised() -> Vec<&'static str> {
    METHODS
        .iter()
        .copied()
        .filter(|m| *m != super::schema::CANCEL_METHOD && registry_has(m))
        .collect()
}

fn registry_has(method: &str) -> bool {
    REGISTRY.iter().any(|(name, _, _)| *name == method)
}

/// Registered callable handlers. The complete METHODS inventory remains the
/// wire authority; resolve returns callable entries only for implemented
/// families.
const REGISTRY: &[(&str, ParamsValidator, Handler)] = &[
    (
        "iprange.v1.system.describe",
        super::handlers::system::validate_describe_params,
        super::handlers::system::describe,
    ),
    (
        "iprange.v1.reader.open",
        super::handlers::reader::validate_reader_source,
        super::handlers::reader::open,
    ),
    (
        "iprange.v1.reader.close",
        super::handlers::reader::validate_reader_handle,
        super::handlers::reader::close,
    ),
    (
        "iprange.v1.reader.info",
        super::handlers::reader::validate_reader_handle,
        super::handlers::reader::info,
    ),
    (
        "iprange.v1.reader.metadata",
        super::handlers::reader::validate_metadata,
        super::handlers::reader::metadata,
    ),
    (
        "iprange.v1.reader.lookup",
        super::handlers::reader::validate_lookup,
        super::handlers::reader::lookup,
    ),
    (
        "iprange.v1.reader.feeds.open",
        super::handlers::cursors::validate_feeds_open,
        super::handlers::cursors::feeds_open,
    ),
    (
        "iprange.v1.reader.feeds.next",
        super::handlers::cursors::validate_cursor,
        super::handlers::cursors::feeds_next,
    ),
    (
        "iprange.v1.reader.feeds.close",
        super::handlers::cursors::validate_cursor,
        super::handlers::cursors::feeds_close,
    ),
    (
        "iprange.v1.reader.matching_feeds",
        super::handlers::reader::validate_matching_feeds,
        super::handlers::reader::matching_feeds,
    ),
    (
        "iprange.v1.reader.ranges.open",
        super::handlers::cursors::validate_ranges_open,
        super::handlers::cursors::ranges_open,
    ),
    (
        "iprange.v1.reader.ranges.next",
        super::handlers::cursors::validate_cursor,
        super::handlers::cursors::ranges_next,
    ),
    (
        "iprange.v1.reader.ranges.close",
        super::handlers::cursors::validate_cursor,
        super::handlers::cursors::ranges_close,
    ),
    (
        "iprange.v1.database.info",
        super::handlers::reader::validate_reader_source,
        super::handlers::reader::database_info,
    ),
    (
        "iprange.v1.database.metadata.get",
        super::handlers::reader::validate_database_metadata,
        super::handlers::reader::database_metadata,
    ),
    (
        "iprange.v1.current.publish",
        super::handlers::publish::validate_current_publish,
        super::handlers::publish::current_publish,
    ),
    (
        "iprange.v1.database.create",
        super::handlers::lifecycle::validate_database_create,
        super::handlers::lifecycle::database_create,
    ),
    (
        "iprange.v1.database.metadata.replace",
        super::handlers::lifecycle::validate_database_metadata_replace,
        super::handlers::lifecycle::database_metadata_replace,
    ),
    (
        "iprange.v1.export",
        super::handlers::export::validate_export,
        super::handlers::export::export,
    ),
    (
        "iprange.v1.snapshot",
        super::handlers::snapshot::validate_snapshot,
        super::handlers::snapshot::snapshot,
    ),
    (
        "iprange.v1.algebra.compare",
        super::handlers::algebra::validate_algebra_compare,
        super::handlers::algebra::algebra_compare,
    ),
    (
        "iprange.v1.algebra.count",
        super::handlers::algebra::validate_algebra_count,
        super::handlers::algebra::algebra_count,
    ),
    (
        "iprange.v1.algebra.publish",
        super::handlers::algebra::validate_algebra_publish,
        super::handlers::algebra::algebra_publish,
    ),
    (
        "iprange.v1.commit.resolve",
        super::handlers::live::validate_commit_resolve,
        super::handlers::live::commit_resolve,
    ),
    (
        "iprange.v1.database.create.resolve",
        super::handlers::live::validate_create_resolve,
        super::handlers::live::create_resolve,
    ),
    (
        "iprange.v1.database.initialize_live",
        super::handlers::live::validate_database_initialize_live,
        super::handlers::live::database_initialize_live,
    ),
    (
        "iprange.v1.database.live_residue.resolve",
        super::handlers::live::validate_live_residue_resolve,
        super::handlers::live::live_residue_resolve,
    ),
    (
        "iprange.v1.database.live_transition.resolve",
        super::handlers::live::validate_live_transition_resolve,
        super::handlers::live::live_transition_resolve,
    ),
    (
        "iprange.v1.database.reclaim",
        super::handlers::maintenance::validate_database_reclaim,
        super::handlers::maintenance::database_reclaim,
    ),
    (
        "iprange.v1.database.reset_live",
        super::handlers::live::validate_database_reset_live,
        super::handlers::live::database_reset_live,
    ),
    (
        "iprange.v1.direct.replace",
        super::handlers::live::validate_direct_replace,
        super::handlers::live::direct_replace,
    ),
    (
        "iprange.v1.feeds.create",
        super::handlers::feeds::validate_feeds_create,
        super::handlers::feeds::feeds_create,
    ),
    (
        "iprange.v1.feeds.delete",
        super::handlers::feeds::validate_feeds_delete,
        super::handlers::feeds::feeds_delete,
    ),
    (
        "iprange.v1.feeds.import",
        super::handlers::feeds::validate_feeds_import,
        super::handlers::feeds::feeds_import,
    ),
    (
        "iprange.v1.feeds.rename",
        super::handlers::feeds::validate_feeds_rename,
        super::handlers::feeds::feeds_rename,
    ),
    (
        "iprange.v1.feeds.replace",
        super::handlers::feeds::validate_feeds_replace,
        super::handlers::feeds::feeds_replace,
    ),
    (
        "iprange.v1.history.project",
        super::handlers::algebra::validate_history_project,
        super::handlers::algebra::history_project,
    ),
    (
        "iprange.v1.join.direct",
        super::handlers::algebra::validate_join_direct,
        super::handlers::algebra::join_direct,
    ),
    (
        "iprange.v1.join.membership",
        super::handlers::algebra::validate_join_membership,
        super::handlers::algebra::join_membership,
    ),
    (
        "iprange.v1.maintenance.list",
        super::handlers::maintenance::validate_maintenance_list,
        super::handlers::maintenance::maintenance_list,
    ),
    (
        "iprange.v1.maintenance.remove",
        super::handlers::maintenance::validate_maintenance_remove,
        super::handlers::maintenance::maintenance_remove,
    ),
    (
        "iprange.v1.publication.inspect",
        super::handlers::maintenance::validate_publication_inspect,
        super::handlers::maintenance::publication_inspect,
    ),
    (
        "iprange.v1.publication.residue.remove",
        super::handlers::maintenance::validate_publication_residue_remove,
        super::handlers::maintenance::publication_residue_remove,
    ),
    (
        "iprange.v1.publication.resolve",
        super::handlers::maintenance::validate_publication_resolve,
        super::handlers::maintenance::publication_resolve,
    ),
    (
        "iprange.v1.query.cardinalities",
        super::handlers::algebra::validate_query_cardinalities,
        super::handlers::algebra::query_cardinalities,
    ),
    (
        "iprange.v1.query.matching_feeds",
        super::handlers::algebra::validate_query_matching_feeds,
        super::handlers::algebra::query_matching_feeds,
    ),
    (
        "iprange.v1.query.overlaps",
        super::handlers::algebra::validate_query_overlaps,
        super::handlers::algebra::query_overlaps,
    ),
    (
        "iprange.v1.recover",
        super::handlers::recovery::validate_recover,
        super::handlers::recovery::recover,
    ),
    (
        "iprange.v1.recovery.inspect",
        super::handlers::recovery::validate_recovery_inspect,
        super::handlers::recovery::recovery_inspect,
    ),
    (
        "iprange.v1.retention.first_seen.refresh",
        super::handlers::live::validate_first_seen_refresh,
        super::handlers::live::first_seen_refresh,
    ),
    (
        "iprange.v1.retention.last_seen.refresh",
        super::handlers::live::validate_last_seen_refresh,
        super::handlers::live::last_seen_refresh,
    ),
    (
        "iprange.v1.validate",
        super::handlers::recovery::validate_validate,
        super::handlers::recovery::validate,
    ),
];

pub fn resolve(method: &str) -> Option<(ParamsValidator, Handler)> {
    REGISTRY
        .iter()
        .find(|(name, _, _)| *name == method)
        .map(|(_, validator, handler)| (*validator, *handler))
}
