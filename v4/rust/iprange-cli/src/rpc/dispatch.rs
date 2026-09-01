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
    #[allow(dead_code)] // used by handler families added in later delivery steps
    pub fn new(code: &'static str, outcome: &'static str, message: impl Into<String>) -> Self {
        Self { code, outcome, message: message.into(), details: None }
    }
    #[allow(dead_code)] // used by handler families added in later delivery steps
    pub fn invalid_params(message: impl Into<String>) -> Self {
        Self::new("invalid_argument", "not_started", message)
    }
}

/// A request handler: params in, complete converted result out.
pub type Handler = fn(state: &mut super::session::SessionState, params: Value) -> Result<Value, HandlerError>;

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

/// Methods advertised by system.describe: every registry entry except
/// the cancel notification (which is not callable).
pub fn advertised() -> Vec<&'static str> {
    METHODS.iter().copied().filter(|m| *m != super::schema::CANCEL_METHOD).collect()
}

/// Registered handlers. system.describe is the only handler in the
/// transport milestone; families are added in the fixed delivery
/// order with qualification at each step.
const REGISTRY: &[(&str, ParamsValidator, Handler)] = &[
    ("iprange.v1.system.describe",
     super::handlers::system::validate_describe_params,
     super::handlers::system::describe),
];

pub fn resolve(method: &str) -> Option<(ParamsValidator, Handler)> {
    REGISTRY.iter().find(|(name, _, _)| *name == method).map(|(_, validator, handler)| (*validator, *handler))
}

