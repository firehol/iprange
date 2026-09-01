"""Result schemas for every registered method (iprange-jsonrpc-v1.md).

Modeling rules (recorded in SOW-0028):

- Every result begins with `method` whose value is the exact method
  name; validate_result() enforces the echo.
- Top-level fields are the snake_case conversion of the public SDK
  result type (Rust iprange-livedb types are semantic authority).
  Depth is one: nested composite SDK types stay opaque objects unless
  the JSON-RPC spec documents their members (logical_change, budgets,
  file identities, output facts, addresses).
- u64/cardinality/identity counters are canonical unsigned decimal
  strings; u32 values are JSON integers; [u8; 16] identities are 32
  lowercase hex; value tags in results always use {"hex": ...}.
- Plain SDK enums (state, status, outcome, location, policy, ...)
  convert to lowercase snake_case strings; their value sets are not
  re-listed here because iprange-jsonrpc-v1.md does not enumerate them.
- Enum variants that carry payloads (ReclaimResult) convert to an
  object with an explicit lowercase `kind` discriminator.
- SDK `cause` is never a success field (spec: it becomes the error
  message).
- Optional SDK fields are absent, not null.
"""
from . import common as C

# Common mechanical-conversion building blocks.
FILE_IDENTITY = {
    "type": "object",
    "properties": {"volume": C.U64, "file": C.U64},
    "required": ["volume", "file"],
    "additional": False,
}
RESULT_VALUE_TAG = {
    "type": "object",
    "properties": {"hex": C.VALUE_TAG_HEX},
    "required": ["hex"],
    "additional": False,
}
HEX16 = {"type": "string", "hex": 32}
OPAQUE = {"type": "object"}
OPAQUE_LIST = {"type": "array", "items": OPAQUE}
SIGNED32 = {"type": "integer", "min": -2147483648, "max": 2147483647}
STRING = {"type": "string"}
BOOL = {"type": "boolean"}

NETWORK_ENRICHMENT_VALUE = {
    "type": "object",
    "properties": {
        "asn": C.U32,
        "country_id": C.U32,
        "state_id": C.U32,
        "city_id": C.U32,
        "location": {
            "type": "one_of",
            "options": [
                {"type": "null"},
                {"type": "object",
                 "properties": {"latitude_microdegrees": SIGNED32,
                                "longitude_microdegrees": SIGNED32},
                 "required": ["latitude_microdegrees", "longitude_microdegrees"],
                 "additional": False},
            ],
        },
        "threat_feeds": {"type": "array", "items": C.FEED_NAME},
    },
    "required": ["asn", "country_id", "state_id", "city_id", "location", "threat_feeds"],
    "additional": False,
}

# A generic ranges.next schema cannot know the view that opened the cursor,
# but each record must still be exactly one complete wire shape.  The runner
# cross-checks the selected shape against the opening request.
ADDRESS_RANGE = {
    "type": "one_of",
    "options": [
        {
            "type": "object",
            "properties": {
                "from": C.IP_ADDRESS,
                "to": C.IP_ADDRESS,
                "value": C.U32,
            },
            "required": ["from", "to", "value"],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {
                "from": C.IP_ADDRESS,
                "to": C.IP_ADDRESS,
                "value": NETWORK_ENRICHMENT_VALUE,
            },
            "required": ["from", "to", "value"],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {
                "from": C.IP_ADDRESS,
                "to": C.IP_ADDRESS,
            },
            "required": ["from", "to"],
            "additional": False,
        },
    ],
}
# Lookup facts are exhaustive by database kind.  This prevents a direct value
# from being polluted with membership feeds or enrichment fields, and prevents
# an absent fact from carrying any kind-specific payload.
LOOKUP_MATCH = {
    "type": "one_of",
    "options": [
        {
            "type": "object",
            "properties": {
                "address": C.IP_ADDRESS,
                "present": BOOL,
                "value": C.U32,
            },
            "required": ["address", "present", "value"],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {
                "address": C.IP_ADDRESS,
                "present": BOOL,
                "feeds": {"type": "array", "items": C.FEED_NAME},
            },
            "required": ["address", "present", "feeds"],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {
                "address": C.IP_ADDRESS,
                "present": BOOL,
                "asn": C.U32,
                "country_id": C.U32,
                "state_id": C.U32,
                "city_id": C.U32,
                "location": {
                    "type": "one_of",
                    "options": [
                        {"type": "null"},
                        {
                            "type": "object",
                            "properties": {
                                "latitude_microdegrees": SIGNED32,
                                "longitude_microdegrees": SIGNED32,
                            },
                            "required": ["latitude_microdegrees", "longitude_microdegrees"],
                            "additional": False,
                        },
                    ],
                },
                "threat_feeds": {"type": "array", "items": C.FEED_NAME},
            },
            "required": [
                "address", "present", "asn", "country_id", "state_id", "city_id",
                "location", "threat_feeds",
            ],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {
                "address": C.IP_ADDRESS,
                "present": BOOL,
            },
            "required": ["address", "present"],
            "additional": False,
        },
    ],
}

OUTPUT_FACTS = {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "sha256": {"type": "string", "hex": 64},
        "bytes": {"type": "string", "decimal": True},
        "rows": {"type": "string", "decimal": True},
    },
    "required": ["path", "sha256", "bytes", "rows"],
    "additional": False,
}
# DatabaseInfo (reader_core.rs) mechanical conversion.
DATABASE_INFO = {
    "type": "object",
    "properties": {
        "address_family": C.FAMILY,
        "value_kind": C.VALUE_KIND,
        "structure_kind": C.STRUCTURE_KIND,
        "value_tag": RESULT_VALUE_TAG,
        "database_id": HEX16,
        "transaction_id": C.U64,
        "commit_nonce": HEX16,
        "page_count": C.U64,
        "range_record_count": C.U64,
        "active_feed_count": C.U64,
        "meta_selection": STRING,
    },
    "required": ["address_family", "value_kind", "structure_kind", "value_tag",
                 "database_id", "transaction_id", "commit_nonce", "page_count",
                 "range_record_count", "active_feed_count", "meta_selection"],
    "additional": False,
}
# CommitResult (live_writer/result.rs) mechanical conversion.
COMMIT_RESULT = {
    "type": "object",
    "properties": {
        "attempted_database_id": HEX16,
        "directory_identity": FILE_IDENTITY,
        "main_identity": FILE_IDENTITY,
        "attempted_transaction_id": C.U64,
        "attempted_commit_nonce": HEX16,
        "durability": STRING,
        "cleanup": OPAQUE,
        "coordination_cleanup": OPAQUE,
    },
    "required": ["attempted_database_id", "directory_identity", "main_identity",
                 "attempted_transaction_id", "attempted_commit_nonce", "durability",
                 "cleanup", "coordination_cleanup"],
    "additional": False,
}
# CloseResult (live_writer/result.rs) mechanical conversion.
CLOSE_RESULT = {
    "type": "object",
    "properties": {
        "outcome": STRING,
        "abort_outcome": STRING,
        "cleanup": OPAQUE,
        "coordination_cleanup": OPAQUE,
    },
    "required": ["outcome", "cleanup", "coordination_cleanup"],
    "additional": False,
}
# Multi-reader methods report every live close in reader order.
CLOSE_RESULT_LIST = {
    "type": "array",
    "items": CLOSE_RESULT,
}

# PublicationResult (publication/types.rs) mechanical conversion.
PUBLICATION_RESULT = {
    "type": "object",
    "properties": {
        "attempt": STRING,
        "main_namespace_may_have_been_attempted": BOOL,
        "publication": STRING,
        "destination_content": STRING,
        "later_canonical": STRING,
        "live_lineage": OPAQUE,
        "later_attempt_or_sidecar_id": HEX16,
        "later_selected_transaction_id": C.U64,
        "later_selected_commit_nonce": HEX16,
        "main_access_policy": STRING,
        "coordination_access_policy": STRING,
        "cleanup": OPAQUE,
        "coordination_cleanup": OPAQUE,
        "housekeeping": OPAQUE,
        "visible_housekeeping": OPAQUE_LIST,
    },
    "required": ["attempt", "main_namespace_may_have_been_attempted", "publication",
                 "destination_content", "later_canonical", "main_access_policy",
                 "coordination_access_policy", "cleanup", "coordination_cleanup",
                 "housekeeping", "visible_housekeeping"],
    "additional": False,
}
# WorkflowReport (workflow.rs) mechanical conversion.
WORKFLOW_REPORT = {
    "type": "object",
    "properties": {
        "workflow": STRING,
        "logical_change": C.LOGICAL_CHANGE,
        "input_record_count": C.U64,
        "input_normalized_interval_count": C.U64,
        "before_range_record_count": C.U64,
        "after_range_record_count": C.U64,
        "input_addresses": C.U64,
        "before_addresses": C.U64,
        "after_addresses": C.U64,
        "unchanged_value_addresses": C.U64,
        "changed_value_addresses": C.U64,
        "added_addresses": C.U64,
        "removed_addresses": C.U64,
        "source_feed_count": C.U64,
        "matched_feed_count": C.U64,
        "created_feed_count": C.U64,
        "source_distinct_membership_count": C.U64,
        "translated_membership_count": C.U64,
    },
    "required": ["workflow", "logical_change", "input_record_count",
                 "input_normalized_interval_count", "before_range_record_count",
                 "after_range_record_count", "input_addresses", "before_addresses",
                 "after_addresses", "unchanged_value_addresses",
                 "changed_value_addresses", "added_addresses", "removed_addresses",
                 "source_feed_count", "matched_feed_count", "created_feed_count",
                 "source_distinct_membership_count", "translated_membership_count"],
    "additional": False,
}
# ImmutableFeedReport (immutable_feed.rs) mechanical conversion.
IMMUTABLE_FEED_REPORT = {
    "type": "object",
    "properties": {
        "input_record_count": C.U64,
        "normalized_interval_count": C.U64,
        "addresses": C.U64,
    },
    "required": ["input_record_count", "normalized_interval_count", "addresses"],
    "additional": False,
}
# MembershipAggregationReport (membership_query/aggregation.rs).
MEMBERSHIP_AGGREGATION_REPORT = {
    "type": "object",
    "properties": {
        "scanned_range_count": C.U64,
        "scanned_addresses": C.U64,
        "feed_result_count": C.U64,
        "pair_result_count": C.U64,
    },
    "required": ["scanned_range_count", "scanned_addresses", "feed_result_count",
                 "pair_result_count"],
    "additional": False,
}
# DirectJoinReport (membership_query/join.rs).
DIRECT_JOIN_REPORT = {
    "type": "object",
    "properties": {
        "membership_range_count": C.U64,
        "direct_ranges_visited": C.U64,
        "joined_segment_count": C.U64,
        "selected_addresses": C.U64,
        "mapped_addresses": C.U64,
        "unmapped_addresses": C.U64,
        "result_cell_count": C.U64,
    },
    "required": ["membership_range_count", "direct_ranges_visited", "joined_segment_count",
                 "selected_addresses", "mapped_addresses", "unmapped_addresses",
                 "result_cell_count"],
    "additional": False,
}
# MembershipJoinReport (membership_query/join.rs).
MEMBERSHIP_JOIN_REPORT = {
    "type": "object",
    "properties": {
        "left_range_count": C.U64,
        "right_range_count": C.U64,
        "joined_segment_count": C.U64,
        "left_addresses": C.U64,
        "right_addresses": C.U64,
        "overlap_addresses": C.U64,
        "left_uncovered_addresses": C.U64,
        "right_uncovered_addresses": C.U64,
        "cross_result_count": C.U64,
        "uncovered_result_count": C.U64,
    },
    "required": ["left_range_count", "right_range_count", "joined_segment_count",
                 "left_addresses", "right_addresses", "overlap_addresses",
                 "left_uncovered_addresses", "right_uncovered_addresses",
                 "cross_result_count", "uncovered_result_count"],
    "additional": False,
}
# CreateResult (live_lifecycle/creation.rs) mechanical conversion.
CREATE_RESULT = {
    "type": "object",
    "properties": {
        "address_family": C.FAMILY,
        "value_kind": C.VALUE_KIND,
        "structure_kind": C.STRUCTURE_KIND,
        "value_tag": RESULT_VALUE_TAG,
        "database_id": HEX16,
        "commit_nonce": HEX16,
        "sidecar_id": HEX16,
        "directory_identity": FILE_IDENTITY,
        "main_basename": STRING,
        "main_identity": FILE_IDENTITY,
        "sidecar_identity": FILE_IDENTITY,
        "reader_capacity": C.U32,
        "state": STRING,
        "residue_possible": BOOL,
        "housekeeping": OPAQUE,
        "visible_housekeeping": OPAQUE_LIST,
    },
    "required": ["address_family", "value_kind", "structure_kind", "value_tag",
                 "database_id", "commit_nonce", "sidecar_id", "main_basename",
                 "reader_capacity", "state", "residue_possible", "housekeeping",
                 "visible_housekeeping"],
    "additional": False,
}
# LiveTransitionResult (live_lifecycle.rs) mechanical conversion.
LIVE_TRANSITION_RESULT = {
    "type": "object",
    "properties": {
        "operation": STRING,
        "reset_policy": STRING,
        "status": STRING,
        "database_id": HEX16,
        "transaction_id": C.U64,
        "commit_nonce": HEX16,
        "directory_identity": FILE_IDENTITY,
        "main_identity": FILE_IDENTITY,
        "main_basename": STRING,
        "reader_capacity": C.U32,
        "sidecar_id": HEX16,
        "previous_sidecar_identity": FILE_IDENTITY,
        "new_sidecar_identity": FILE_IDENTITY,
        "new_sidecar_location": STRING,
        "residue_possible": BOOL,
        "housekeeping": OPAQUE,
        "visible_housekeeping": OPAQUE_LIST,
    },
    "required": ["operation", "status", "database_id", "transaction_id", "commit_nonce",
                 "directory_identity", "main_identity", "main_basename",
                 "reader_capacity", "sidecar_id", "new_sidecar_location",
                 "residue_possible", "housekeeping", "visible_housekeeping"],
    "additional": False,
}
# LiveResidueResult (live_lifecycle/residue.rs) mechanical conversion.
LIVE_RESIDUE_RESULT = {
    "type": "object",
    "properties": {
        "status": STRING,
        "kind": STRING,
        "database_id": HEX16,
        "sidecar_id": HEX16,
        "reader_capacity": C.U32,
        "main_identity": FILE_IDENTITY,
        "sidecar_identity": FILE_IDENTITY,
        "residue_possible": BOOL,
        "housekeeping": OPAQUE,
        "visible_housekeeping": OPAQUE_LIST,
    },
    "required": ["status", "residue_possible", "housekeeping", "visible_housekeeping"],
    "additional": False,
}
# CommitResolutionResult (commit_resolution.rs) mechanical conversion.
COMMIT_RESOLUTION_RESULT = {
    "type": "object",
    "properties": {
        "attempted_database_id": HEX16,
        "attempted_transaction_id": C.U64,
        "attempted_commit_nonce": HEX16,
        "actual_directory_identity": FILE_IDENTITY,
        "actual_main_identity": FILE_IDENTITY,
        "local_file_relation": STRING,
        "resolution": STRING,
        "cleanup": OPAQUE,
        "coordination_cleanup": OPAQUE,
    },
    "required": ["attempted_database_id", "attempted_transaction_id",
                 "attempted_commit_nonce", "actual_directory_identity",
                 "actual_main_identity", "local_file_relation", "resolution",
                 "cleanup", "coordination_cleanup"],
    "additional": False,
}
# RecoveryReport (recovery/report.rs) mechanical conversion.
RECOVERY_REPORT = {
    "type": "object",
    "properties": {
        "pages": OPAQUE,
        "ranges": OPAQUE,
        "catalog_entries": OPAQUE,
        "membership_entries": OPAQUE,
        "structure_entries": OPAQUE,
        "metadata_chunks": OPAQUE,
        "retirement_records": OPAQUE,
        "verified_addresses": C.U64,
        "rejected_addresses": C.U64,
        "bounded_possible_span_addresses": C.U64,
        "has_unbounded_unknown": BOOL,
        "unknown_envelopes": C.U64,
    },
    "required": ["pages", "ranges", "catalog_entries", "membership_entries",
                 "structure_entries", "metadata_chunks", "retirement_records",
                 "verified_addresses", "rejected_addresses",
                 "bounded_possible_span_addresses", "has_unbounded_unknown",
                 "unknown_envelopes"],
    "additional": False,
}
# PublicationResidueInspection (publication/residue.rs) conversion.
PUBLICATION_RESIDUE_INSPECTION = {
    "type": "object",
    "properties": {
        "directory_identity": FILE_IDENTITY,
        "coordination_identity": FILE_IDENTITY,
        "coordination": STRING,
        "publication": PUBLICATION_RESULT,
        "handle": OPAQUE,
    },
    "required": ["directory_identity", "coordination"],
    "additional": False,
}
# PublicationResidueRemoval (publication/residue.rs) conversion.
PUBLICATION_RESIDUE_REMOVAL = {
    "type": "object",
    "properties": {
        "directory_identity": FILE_IDENTITY,
        "coordination_identity": FILE_IDENTITY,
        "main": OPAQUE,
        "later_coordination": OPAQUE,
        "coordination_access_policy": STRING,
        "cleanup": OPAQUE,
        "coordination_cleanup": OPAQUE,
        "housekeeping": OPAQUE,
        "visible_housekeeping": OPAQUE_LIST,
        "handle": OPAQUE,
    },
    "required": ["directory_identity", "coordination_identity", "later_coordination",
                 "coordination_access_policy", "cleanup", "coordination_cleanup",
                 "housekeeping", "visible_housekeeping"],
    "additional": False,
}
# HistoryWindowReport + HistoryProjectionReport (history.rs) conversion.
HISTORY_WINDOW_REPORT = {
    "type": "object",
    "properties": {
        "feed_name": C.FEED_NAME,
        "cutoff": C.U32,
        "created": BOOL,
        "before_interval_count": C.U64,
        "after_interval_count": C.U64,
        "before_addresses": C.U64,
        "after_addresses": C.U64,
        "unchanged_addresses": C.U64,
        "added_addresses": C.U64,
        "removed_addresses": C.U64,
    },
    "required": ["feed_name", "cutoff", "created", "before_interval_count",
                 "after_interval_count", "before_addresses", "after_addresses",
                 "unchanged_addresses", "added_addresses", "removed_addresses"],
    "additional": False,
}
HISTORY_PROJECTION_REPORT = {
    "type": "object",
    "properties": {
        "logical_change": C.LOGICAL_CHANGE,
        "source_range_count": C.U64,
        "source_addresses": C.U64,
        "created_feed_count": C.U64,
        "before_interval_count": C.U64,
        "after_interval_count": C.U64,
        "before_addresses": C.U64,
        "after_addresses": C.U64,
        "unchanged_addresses": C.U64,
        "added_addresses": C.U64,
        "removed_addresses": C.U64,
        "windows": {"type": "array", "items": HISTORY_WINDOW_REPORT},
    },
    "required": ["logical_change", "source_range_count", "source_addresses",
                 "created_feed_count", "before_interval_count", "after_interval_count",
                 "before_addresses", "after_addresses", "unchanged_addresses",
                 "added_addresses", "removed_addresses", "windows"],
    "additional": False,
}

RESULTS = {}


def _register(method, body):
    RESULTS[method] = body


def _result(required_extra=(), body=None):
    schema = {
        "type": "object",
        "properties": {"method": {"type": "string"}},
        "required": ["method"] + list(required_extra),
        "additional": False,
    }
    if body:
        schema["properties"].update(body["properties"])
        schema["required"] += body.get("required", [])
    return schema


def _optional(name, sub):
    """One top-level optional member (Option<T> in the SDK)."""
    return {name: sub}


def _field(name, sub, required=True):
    return {name: sub}, required


# system.describe (fully documented by iprange-jsonrpc-v1.md)
_register("iprange.v1.system.describe", _result(required_extra=(), body={
    "type": "object",
    "properties": {
        "product": {"type": "string"},
        "product_version": {"type": "string"},
        "implementation": {"type": "string", "enum": ["rust", "go"]},
        "jsonrpc_version": {"type": "string"},
        "api_version": {"type": "string"},
        "format": {"type": "string", "enum": ["iprange-v4-phase1-unsigned"]},
        "platform": {"type": "string", "enum": ["linux", "macos", "windows", "freebsd", "other"]},
        "families": {"type": "array", "items": C.FAMILY, "min": 1, "unique": True},
        "methods": {"type": "array", "items": {"type": "string"}, "min": 1},
        "export_formats": {
            "type": "array",
            "items": {"type": "string", "enum": ["netset", "ipset", "ranges", "csv",
                                                   "jsonl", "legacy_binary"]},
            "min": 1,
            "unique": True,
        },
        "limits": {
            "type": "object",
            "properties": {
                "input_frame_bytes": C.U64,
                "output_frame_bytes": C.U64,
                "response_object_bytes": C.U64,
                "batch_requests": C.U32,
                "queued_requests": C.U32,
                "reader_handles": C.U32,
                "cursor_handles": C.U32,
                "lookup_addresses": C.U32,
                "cursor_records": C.U32,
            },
            "required": ["input_frame_bytes", "output_frame_bytes", "response_object_bytes",
                         "batch_requests", "queued_requests", "reader_handles",
                         "cursor_handles", "lookup_addresses", "cursor_records"],
            "additional": False,
        },
        "fault_worker": {
            "type": "object",
            "properties": {"available": {"type": "boolean"}, "protocol": {"type": "string"}},
            "required": ["available", "protocol"],
            "additional": False,
        },
        "platform_result_fields": {"type": "array", "items": {"type": "string"}},
    },
    "required": ["product", "product_version", "implementation", "jsonrpc_version",
                 "api_version", "format", "platform", "families", "methods",
                 "export_formats", "limits", "fault_worker", "platform_result_fields"],
}))

# reader family
_register("iprange.v1.reader.open", _result(required_extra=("reader", "info"), body={
    "type": "object",
    "properties": {"reader": C.HANDLE, "info": DATABASE_INFO},
    "required": ["reader", "info"],
}))
_register("iprange.v1.reader.close", _result(required_extra=("closed",), body={
    "type": "object",
    "properties": {
        "closed": BOOL,
        # Live readers only: complete live close result conversion.
        "source_close": CLOSE_RESULT,
    },
    "required": ["closed"],
}))
_register("iprange.v1.reader.info", _result(required_extra=("info",), body={
    "type": "object", "properties": {"info": DATABASE_INFO}, "required": ["info"],
}))
_register("iprange.v1.reader.metadata", _result(required_extra=("present",), body={
    "type": "object",
    "properties": {
        "present": BOOL,
        "base64": {"type": "string", "base64": True},
        "output": OUTPUT_FACTS,
    },
    "required": ["present"],
}))
_register("iprange.v1.reader.lookup", _result(required_extra=("matches",), body={
    "type": "object",
    "properties": {"matches": {"type": "array", "items": LOOKUP_MATCH, "min": 1}},
    "required": ["matches"],
}))
_register("iprange.v1.reader.feeds.open", _result(required_extra=("cursor",), body={
    "type": "object", "properties": {"cursor": C.HANDLE}, "required": ["cursor"],
}))
_register("iprange.v1.reader.feeds.next", _result(required_extra=("feeds", "done"), body={
    "type": "object",
    "properties": {
        "feeds": {"type": "array",
                  "items": {"type": "object",
                            "properties": {"name": C.FEED_NAME},
                            "required": ["name"], "additional": False}},
        "done": BOOL,
    },
    "required": ["feeds", "done"],
}))
_register("iprange.v1.reader.feeds.close", _result(required_extra=("closed",), body={
    "type": "object", "properties": {"closed": BOOL}, "required": ["closed"],
}))
_register("iprange.v1.reader.matching_feeds", _result(
    required_extra=("address", "feeds", "matching_feed_count"), body={
        "type": "object",
        "properties": {
            "address": C.IP_ADDRESS,
            "feeds": {"type": "array", "items": C.FEED_NAME},
            # MatchingFeedsReport conversion.
            "matching_feed_count": C.U64,
        },
        "required": ["address", "feeds", "matching_feed_count"],
    }))
_register("iprange.v1.reader.ranges.open", _result(required_extra=("cursor",), body={
    "type": "object", "properties": {"cursor": C.HANDLE}, "required": ["cursor"],
}))
_register("iprange.v1.reader.ranges.next", _result(required_extra=("records", "done"), body={
    "type": "object",
    "properties": {
        "records": {"type": "array", "items": ADDRESS_RANGE},
        "done": BOOL,
    },
    "required": ["records", "done"],
}))
_register("iprange.v1.reader.ranges.close", _result(required_extra=("closed",), body={
    "type": "object", "properties": {"closed": BOOL}, "required": ["closed"],
}))

# database lifecycle
_register("iprange.v1.database.create", _result(required_extra=(), body={
    "type": "object",
    "properties": CREATE_RESULT["properties"],
    "required": CREATE_RESULT["required"],
}))
_register("iprange.v1.database.initialize_live", _result(required_extra=(), body={
    "type": "object",
    "properties": LIVE_TRANSITION_RESULT["properties"],
    "required": LIVE_TRANSITION_RESULT["required"],
}))
_register("iprange.v1.database.reset_live", _result(required_extra=(), body={
    "type": "object",
    "properties": LIVE_TRANSITION_RESULT["properties"],
    "required": LIVE_TRANSITION_RESULT["required"],
}))
_register("iprange.v1.database.create.resolve", _result(required_extra=(), body={
    "type": "object",
    "properties": CREATE_RESULT["properties"],
    "required": CREATE_RESULT["required"],
}))
_register("iprange.v1.database.live_transition.resolve", _result(required_extra=(), body={
    "type": "object",
    "properties": LIVE_TRANSITION_RESULT["properties"],
    "required": LIVE_TRANSITION_RESULT["required"],
}))
_register("iprange.v1.database.live_residue.resolve", _result(required_extra=(), body={
    "type": "object",
    "properties": LIVE_RESIDUE_RESULT["properties"],
    "required": LIVE_RESIDUE_RESULT["required"],
}))
_register("iprange.v1.database.reclaim", _result(
    required_extra=("reclamation", "writer_close"), body={
        "type": "object",
        "properties": {
            # ReclaimResult enum conversion: explicit kind discriminator.
            "reclamation": {
                "type": "one_of",
                "options": [
                    {"type": "object",
                     "properties": {"kind": {"type": "string", "enum": ["no_change"]}},
                     "required": ["kind"], "additional": False},
                    {"type": "object",
                     "properties": {
                         "kind": {"type": "string", "enum": ["commit"]},
                         "transaction_count": C.U64,
                         "page_count": C.U64,
                         "commit": COMMIT_RESULT,
                     },
                     "required": ["kind", "transaction_count", "page_count", "commit"],
                     "additional": False},
                ],
            },
            "writer_close": CLOSE_RESULT,
        },
        "required": ["reclamation", "writer_close"],
    }))
_register("iprange.v1.database.info", _result(required_extra=("info",), body={
    "type": "object",
    "properties": {
        "info": DATABASE_INFO,
        # Live sources only: complete live close result conversion.
        "source_close": CLOSE_RESULT,
    },
    "required": ["info"],
}))
_register("iprange.v1.database.metadata.get", _result(required_extra=("present",), body={
    "type": "object",
    "properties": {
        "present": BOOL,
        "base64": {"type": "string", "base64": True},
        "output": OUTPUT_FACTS,
        # Live sources only: complete live close result conversion.
        "source_close": CLOSE_RESULT,
    },
    "required": ["present"],
}))
_register("iprange.v1.database.metadata.replace", _result(
    required_extra=("logical_change", "writer_close"), body={
        "type": "object",
        "properties": {
            "logical_change": C.LOGICAL_CHANGE,
            # Present only when a commit was attempted.
            "commit": COMMIT_RESULT,
            "writer_close": CLOSE_RESULT,
        },
        "required": ["logical_change", "writer_close"],
    }))

# publisher mutations
_PUBLISHER_COMMON = {
    "type": "object",
    "properties": {
        # High-level workflow report (WorkflowReport conversion).
        "report": WORKFLOW_REPORT,
        "metadata_logical_change": C.LOGICAL_CHANGE,
        "commit": COMMIT_RESULT,
        "writer_close": CLOSE_RESULT,
    },
    "required": ["report", "metadata_logical_change", "writer_close"],
}
_register("iprange.v1.current.publish", _result(
    required_extra=("report", "publication"), body={
        "type": "object",
        "properties": {
            "report": IMMUTABLE_FEED_REPORT,
            "publication": PUBLICATION_RESULT,
        },
        "required": ["report", "publication"],
    }))
_register("iprange.v1.direct.replace", _result(required_extra=(), body=_PUBLISHER_COMMON))
_FEED_CHANGE_COMMON = {
    "type": "object",
    "properties": {
        # Delete and rename have no SDK WorkflowReport (product decision
        # D2); the catalog-changing outcome is carried by the commit
        # facts and the always-factual metadata/close facts.
        "metadata_logical_change": C.LOGICAL_CHANGE,
        "commit": COMMIT_RESULT,
        "writer_close": CLOSE_RESULT,
    },
    "required": ["metadata_logical_change", "writer_close"],
}
for _m in ("iprange.v1.feeds.create", "iprange.v1.feeds.replace",
           "iprange.v1.feeds.import"):
    _register(_m, _result(required_extra=(), body=_PUBLISHER_COMMON))
for _m in ("iprange.v1.feeds.delete", "iprange.v1.feeds.rename"):
    _register(_m, _result(required_extra=(), body=_FEED_CHANGE_COMMON))
_register("iprange.v1.retention.first_seen.refresh", _result(required_extra=(), body={
    "type": "object",
    "properties": dict(_PUBLISHER_COMMON["properties"], **{
        "removals": {
            "type": "object",
            "properties": {
                "publication": PUBLICATION_RESULT,
                "output": OUTPUT_FACTS,
            },
            "required": [],
            "additional": False,
        },
    }),
    "required": _PUBLISHER_COMMON["required"],
}))
_register("iprange.v1.retention.last_seen.refresh", _result(required_extra=(), body=_PUBLISHER_COMMON))
_register("iprange.v1.history.project", _result(required_extra=(), body={
    "type": "object",
    "properties": dict(_PUBLISHER_COMMON["properties"], **{
        "report": HISTORY_PROJECTION_REPORT,
        "source_closes": CLOSE_RESULT_LIST,
    }),
    "required": _PUBLISHER_COMMON["required"],
}))

# query family
_register("iprange.v1.query.cardinalities", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": MEMBERSHIP_AGGREGATION_REPORT,
                   # Live sources only: complete live close result conversion.
                   "source_close": CLOSE_RESULT},
    "required": ["output", "report"],
}))
_register("iprange.v1.query.overlaps", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": MEMBERSHIP_AGGREGATION_REPORT,
                   "source_close": CLOSE_RESULT},
    "required": ["output", "report"],
}))
_register("iprange.v1.query.matching_feeds", _result(
    required_extra=("output", "matching_feed_count"), body={
        "type": "object",
        "properties": {"output": OUTPUT_FACTS, "matching_feed_count": C.U64,
                       "source_close": CLOSE_RESULT},
        "required": ["output", "matching_feed_count"],
    }))

# joins
_register("iprange.v1.join.direct", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": DIRECT_JOIN_REPORT,
                   # Live sources only: every live close in reader order.
                   "source_closes": CLOSE_RESULT_LIST},
    "required": ["output", "report"],
}))
_register("iprange.v1.join.membership", _result(required_extra=("output", "report"), body={
    "type": "object",
    "properties": {"output": OUTPUT_FACTS, "report": MEMBERSHIP_JOIN_REPORT,
                   "source_closes": CLOSE_RESULT_LIST},
    "required": ["output", "report"],
}))

# algebra
_register("iprange.v1.algebra.count", _result(required_extra=("report", "cardinality"), body={
    "type": "object",
    "properties": {
        "source_closes": CLOSE_RESULT_LIST,
        "report": {
            "type": "object",
            "properties": {
                "source_count": C.U64,
                "source_range_count": C.U64,
                "joined_segment_count": C.U64,
                "addresses": C.U64,
            },
            "required": ["source_count", "source_range_count", "joined_segment_count", "addresses"],
            "additional": False,
        },
        "cardinality": C.U64,
    },
    "required": ["report", "cardinality"],
}))
_register("iprange.v1.algebra.compare", _result(required_extra=("report",), body={
    "type": "object",
    "properties": {
        "source_closes": CLOSE_RESULT_LIST,
        "report": {
            "type": "object",
            "properties": {
                "source_count": C.U64,
                "source_range_count": C.U64,
                "joined_segment_count": C.U64,
                "left_addresses": C.U64,
                "right_addresses": C.U64,
                "overlap_addresses": C.U64,
                "left_only_addresses": C.U64,
                "right_only_addresses": C.U64,
                "union_addresses": C.U64,
                "equal": BOOL,
            },
            "required": ["source_count", "source_range_count", "joined_segment_count",
                         "left_addresses", "right_addresses", "overlap_addresses",
                         "left_only_addresses", "right_only_addresses", "union_addresses",
                         "equal"],
            "additional": False,
        },
    },
    "required": ["report"],
}))
_register("iprange.v1.algebra.publish", _result(required_extra=("report", "publication"), body={
    "type": "object",
    "properties": {
        "source_closes": CLOSE_RESULT_LIST,
        "report": {
            "type": "object",
            "properties": {
                "source_count": C.U64,
                "source_range_count": C.U64,
                "joined_segment_count": C.U64,
                "output_feed_count": C.U64,
                "output_range_count": C.U64,
                "output_addresses": C.U64,
            },
            "required": ["source_count", "source_range_count", "joined_segment_count",
                         "output_feed_count", "output_range_count", "output_addresses"],
            "additional": False,
        },
        "publication": PUBLICATION_RESULT,
    },
    "required": ["report", "publication"],
}))

# export
_register("iprange.v1.export", _result(
    required_extra=("path", "format", "sha256", "rows", "addresses", "bytes", "identity"), body={
        "type": "object",
        "properties": {
            "path": C.PATH,
            "format": {"type": "string",
                       "enum": ["netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"]},
            "sha256": {"type": "string", "hex": 64},
            "rows": C.U64,
            "addresses": C.U64,
            "bytes": C.U64,
            "identity": FILE_IDENTITY,
            # Live sources only: complete live close result conversion.
            "source_close": CLOSE_RESULT,
        },
        "required": ["path", "format", "sha256", "rows", "addresses", "bytes", "identity"],
    }))

# snapshot / validation / recovery
_register("iprange.v1.snapshot", _result(required_extra=("publication",), body={
    # SnapshotResult conversion: {publication: PublicationResult} nested.
    "type": "object",
    "properties": {"publication": PUBLICATION_RESULT},
    "required": ["publication"],
}))
_register("iprange.v1.validate", _result(required_extra=("result", "findings"), body={
    "type": "object",
    "properties": {
        "result": OPAQUE,      # complete ValidationResult conversion
        "findings": OUTPUT_FACTS,
    },
    "required": ["result", "findings"],
}))
_register("iprange.v1.recovery.inspect", _result(
    required_extra=("source_identity", "progress", "candidates"), body={
        "type": "object",
        "properties": {
            "source_identity": FILE_IDENTITY,
            "progress": OPAQUE,  # complete ValidationProgress conversion
            "candidates": {"type": "array", "items": OPAQUE, "max": 2},
        },
        "required": ["source_identity", "progress", "candidates"],
    }))
_register("iprange.v1.recover", _result(required_extra=("report", "publication"), body={
    # Success is the complete RecoveryResult conversion; preparation
    # failures are -32010 errors whose details carry the failure facts.
    "type": "object",
    "properties": {
        "report": RECOVERY_REPORT,
        "scratch": OPAQUE,
        "publication": PUBLICATION_RESULT,
    },
    "required": ["report", "publication"],
}))

# resolution attempts
_register("iprange.v1.commit.resolve", _result(required_extra=("resolution",), body={
    "type": "object", "properties": {"resolution": COMMIT_RESOLUTION_RESULT},
    "required": ["resolution"],
}))
_register("iprange.v1.publication.inspect", _result(required_extra=("inspection",), body={
    "type": "object", "properties": {"inspection": PUBLICATION_RESIDUE_INSPECTION},
    "required": ["inspection"],
}))
_register("iprange.v1.publication.resolve", _result(required_extra=("publication",), body={
    "type": "object", "properties": {"publication": PUBLICATION_RESULT},
    "required": ["publication"],
}))
_register("iprange.v1.publication.residue.remove", _result(required_extra=("removal",), body={
    "type": "object", "properties": {"removal": PUBLICATION_RESIDUE_REMOVAL},
    "required": ["removal"],
}))

# maintenance
_register("iprange.v1.maintenance.list", _result(required_extra=("output", "reports"), body={
    "type": "object",
    "properties": {
        "output": OUTPUT_FACTS,
        "reports": {"type": "array", "items": OPAQUE, "min": 1},
    },
    "required": ["output", "reports"],
}))
_register("iprange.v1.maintenance.remove", _result(required_extra=("removal",), body={
    "type": "object", "properties": {"removal": OPAQUE}, "required": ["removal"],
}))


def _self_test():
    import json

    from .engine import ValidationError, validate

    assert validate({"hex": "61"}, RESULT_VALUE_TAG) == {"hex": "61"}
    for bad in ({"hex": "00"}, {"hex": "6100"}, {"hex": "6"}):
        try:
            validate(bad, RESULT_VALUE_TAG)
        except ValidationError:
            pass
        else:
            raise AssertionError(f"invalid result tag accepted: {bad!r}")

    describe = {
        "method": "iprange.v1.system.describe",
        "format": "iprange-v4-phase1-unsigned",
        "families": ["ipv4"],
        "export_formats": ["netset"],
        "methods": ["iprange.v1.system.describe"],
        "limits": {
            "input_frame_bytes": "1048576",
            "output_frame_bytes": "1048576",
            "response_object_bytes": "65000",
            "batch_requests": 16,
            "queued_requests": 16,
            "reader_handles": 64,
            "cursor_handles": 64,
            "lookup_addresses": 4096,
            "cursor_records": 4096,
        },
    }
    # validate_system_describe intentionally checks semantics without requiring
    # the full schema's build-variable fields.
    assert validate_system_describe(describe)
    original = json.loads(json.dumps(describe))
    mutations = (
        ("format", "wrong"),
        ("families", ["ipx"]),
        ("families", []),
        ("export_formats", ["unknown"]),
        ("export_formats", []),
    )
    for key, value in mutations:
        invalid = json.loads(json.dumps(original))
        invalid[key] = value
        try:
            validate_system_describe(invalid)
        except ValidationError:
            pass
        else:
            raise AssertionError(f"invalid system.describe {key} accepted")
    invalid = json.loads(json.dumps(original))
    invalid["limits"]["reader_handles"] = 63
    try:
        validate_system_describe(invalid)
    except ValidationError:
        pass
    else:
        raise AssertionError("invalid system.describe limits accepted")


def validate_system_describe(result):
    """Validate semantic system.describe facts not expressed by item schemas."""

    from . import methods
    from .engine import ValidationError

    if result.get("format") != "iprange-v4-phase1-unsigned":
        raise ValidationError(
            "result[iprange.v1.system.describe].format",
            "format must be iprange-v4-phase1-unsigned",
        )
    expected_limits = {
        "input_frame_bytes": "1048576",
        "output_frame_bytes": "1048576",
        "response_object_bytes": "65000",
        "batch_requests": 16,
        "queued_requests": 16,
        "reader_handles": 64,
        "cursor_handles": 64,
        "lookup_addresses": 4096,
        "cursor_records": 4096,
    }
    if result.get("limits") != expected_limits:
        raise ValidationError(
            "result[iprange.v1.system.describe].limits",
            "system.describe limits must match the documented API v1 values",
        )
    families = result.get("families", [])
    if not families or not set(families) <= {"ipv4", "ipv6"}:
        raise ValidationError(
            "result[iprange.v1.system.describe].families",
            "families must be a nonempty subset of ipv4 and ipv6",
        )
    export_formats = result.get("export_formats", [])
    documented = {"netset", "ipset", "ranges", "csv", "jsonl", "legacy_binary"}
    if not export_formats or not set(export_formats) <= documented:
        raise ValidationError(
            "result[iprange.v1.system.describe].export_formats",
            "export_formats must be a nonempty subset of the documented formats",
        )
    advertised = result.get("methods", [])
    if advertised != sorted(set(advertised)):
        raise ValidationError(
            "result[iprange.v1.system.describe].methods",
            "advertised methods must be unique and bytewise sorted",
        )
    unknown = [name for name in advertised if name not in methods.METHODS]
    if unknown:
        raise ValidationError(
            "result[iprange.v1.system.describe].methods",
            f"unknown advertised methods {unknown!r}",
        )
    if methods.CANCEL_METHOD in advertised:
        raise ValidationError(
            "result[iprange.v1.system.describe].methods",
            "cancel is a notification and must not be advertised as callable",
        )
    return result


def validate_result(method, result):
    """Validate one result against its strict schema and method echo.

    Raises ValidationError on schema violations or when the result
    `method` member does not equal the requested method.
    """
    from . import methods
    from .engine import ValidationError, validate
    body = RESULTS.get(method)
    if body is None:
        return result
    validate(result, body, f"result[{method}]")
    if method == "iprange.v1.system.describe":
        validate_system_describe(result)
    if method == "iprange.v1.reader.lookup":
        for index, fact in enumerate(result.get("matches", [])):
            keys = set(fact)
            if fact.get("present") is False:
                if keys != {"address", "present"}:
                    raise ValidationError(
                        f"result[iprange.v1.reader.lookup].matches[{index}]",
                        "an absent lookup fact may contain only address and present",
                    )
            elif fact.get("present") is True:
                kind_keys = keys - {"address", "present"}
                valid = [{"value"}, {"feeds"}, {"asn", "country_id", "state_id", "city_id", "location", "threat_feeds"}]
                if kind_keys not in valid:
                    raise ValidationError(
                        f"result[iprange.v1.reader.lookup].matches[{index}]",
                        "a present lookup fact must carry exactly one value-kind payload",
                    )
            else:
                raise ValidationError(
                    f"result[iprange.v1.reader.lookup].matches[{index}].present",
                    "present must be a boolean selecting an exhaustive fact shape",
                )
    if result.get("method") != method:
        raise ValidationError(
            f"result[{method}].method",
            f"echo {result.get('method')!r} does not match requested method {method!r}")
    return result


if __name__ == "__main__":
    _self_test()
