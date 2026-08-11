/* Generated from the Rust ABI. Do not edit. */


#ifndef IPRANGE_V4_ABI1_H
#define IPRANGE_V4_ABI1_H

/* Generated with cbindgen:0.29.4 */

#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#if defined(_WIN32)
#  if defined(IPRANGE_V4_ABI1_BUILD)
#    define IPRANGE_V4_ABI1_API __declspec(dllexport)
#  else
#    define IPRANGE_V4_ABI1_API __declspec(dllimport)
#  endif
#  define IPRANGE_V4_ABI1_CALL __cdecl
#elif defined(__GNUC__) || defined(__clang__)
#  define IPRANGE_V4_ABI1_API __attribute__((visibility("default")))
#  define IPRANGE_V4_ABI1_CALL
#else
#  define IPRANGE_V4_ABI1_API
#  define IPRANGE_V4_ABI1_CALL
#endif

typedef struct iprange_v4_abi1_error iprange_v4_abi1_error;
typedef struct iprange_v4_abi1_report iprange_v4_abi1_report;
typedef struct iprange_v4_abi1_reader iprange_v4_abi1_reader;
typedef struct iprange_v4_abi1_writer iprange_v4_abi1_writer;
typedef struct iprange_v4_abi1_cursor iprange_v4_abi1_cursor;
typedef struct iprange_v4_abi1_writer_feed_ref iprange_v4_abi1_writer_feed_ref;
typedef struct iprange_v4_abi1_membership_view iprange_v4_abi1_membership_view;
typedef struct iprange_v4_abi1_borrowed_membership_view iprange_v4_abi1_borrowed_membership_view;
typedef struct iprange_v4_abi1_membership_builder iprange_v4_abi1_membership_builder;
typedef struct iprange_v4_abi1_membership_ref iprange_v4_abi1_membership_ref;
typedef struct iprange_v4_abi1_structure_ref iprange_v4_abi1_structure_ref;
typedef struct iprange_v4_abi1_cleanup_guard iprange_v4_abi1_cleanup_guard;
typedef struct iprange_v4_abi1_residue iprange_v4_abi1_residue;
typedef struct iprange_v4_abi1_membership_scope iprange_v4_abi1_membership_scope;
typedef struct iprange_v4_abi1_membership_algebra iprange_v4_abi1_membership_algebra;

/*
 * Generation-1 pointer and ownership contract
 *
 * - Every non-NULL pointer must address suitably aligned readable or writable
 *   storage for the complete object or slice used by the call. A zero-length
 *   slice may use NULL; a nonzero-length slice may not.
 * - Writable caller ranges must not overlap another input, output, opaque
 *   handle, or borrowed engine object used by the same call. The ABI rejects
 *   overlap between declared output arguments. Pointers outside valid caller
 *   allocations cannot be probed portably and violate this contract.
 * - error_output may be NULL. When supplied, it and every fixed output are
 *   initialized to NULL/zero before semantic work. The caller owns each
 *   returned error, report, reader, writer, cursor, view, reference, builder,
 *   cleanup guard, and residue handle until its matching close/destroy call.
 * - Error and report destroy return HANDLE_BUSY while an untaken cleanup
 *   obligation remains. Reader/writer Close may fail and does not free the
 *   unresolved handle. Writer Close aborts pending work; it never commits it.
 * - Callback batches, callback records, borrowed membership views, and error
 *   causes remain engine-owned. They are valid only for their documented
 *   parent lifetime and must never be freed or retained past that lifetime.
 * - Handles documented as caller-serialized must not be used concurrently or
 *   reentered from their callbacks. Different handles may run concurrently
 *   subject to database locking.
 * - Every function returns STATUS_OK or STATUS_ERROR. Numeric error_code and
 *   report facts are stable classifiers; diagnostic text is informational.
 */


#define IPRANGE_V4_ABI1_ABI_VERSION 1

#define IPRANGE_V4_ABI1_STATUS_OK 0

#define IPRANGE_V4_ABI1_STATUS_ERROR 1

#define IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4 4

#define IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV6 6

#define IPRANGE_V4_ABI1_PATH_POSIX_BYTES 1

#define IPRANGE_V4_ABI1_PATH_WINDOWS_UTF16 2

#define IPRANGE_V4_ABI1_VALUE_KIND_DIRECT 1

#define IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP 2

#define IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED 3

#define IPRANGE_V4_ABI1_STRUCTURE_KIND_NONE 0

#define IPRANGE_V4_ABI1_STRUCTURE_KIND_NETWORK_ENRICHMENT_V1 1

#define IPRANGE_V4_ABI1_SOURCE_OUTCOME_BATCH 1

#define IPRANGE_V4_ABI1_SOURCE_OUTCOME_END 2

#define IPRANGE_V4_ABI1_SOURCE_OUTCOME_ERROR 3

#define IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE 1

#define IPRANGE_V4_ABI1_SINK_OUTCOME_STOP 2

#define IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR 3

#define IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD 1

#define IPRANGE_V4_ABI1_CURSOR_DIRECTION_BACKWARD 2

#define IPRANGE_V4_ABI1_OPEN_MODE_IMMUTABLE 1

#define IPRANGE_V4_ABI1_OPEN_MODE_LIVE 2

#define IPRANGE_V4_ABI1_OPEN_MODE_OFFLINE 3

#define IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS 1

#define IPRANGE_V4_ABI1_DESTINATION_POLICY_REPLACE_EXISTING 2

#define IPRANGE_V4_ABI1_DESTINATION_POLICY_REPLACE_EXISTING_NO_ROLLBACK 3

#define IPRANGE_V4_ABI1_LIVE_RESET_POLICY_ROLLBACK_SAFE 1

#define IPRANGE_V4_ABI1_LIVE_RESET_POLICY_DISCARD_PREVIOUS 2

#define IPRANGE_V4_ABI1_RESOLVER_ACTION_COMPLETE 1

#define IPRANGE_V4_ABI1_RESOLVER_ACTION_REMOVE 2

#define IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_REPLACE 1

#define IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_UNION 2

#define IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_DIFFERENCE 3

#define IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_INTERSECTION 4

#define IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_XOR 5

#define IPRANGE_V4_ABI1_MEMBERSHIP_AGGREGATION_CARDINALITIES 1

#define IPRANGE_V4_ABI1_MEMBERSHIP_AGGREGATION_ALL_PAIRS 2

#define IPRANGE_V4_ABI1_MEMBERSHIP_AGGREGATION_TARGET_AGAINST_SCOPE 3

#define IPRANGE_V4_ABI1_MEMBERSHIP_AGGREGATION_SELECTED_PAIRS 4

#define IPRANGE_V4_ABI1_UNCOVERED_SIDE_LEFT 1

#define IPRANGE_V4_ABI1_UNCOVERED_SIDE_RIGHT 2

#define IPRANGE_V4_ABI1_FEED_SELECTION_ALL 1

#define IPRANGE_V4_ABI1_FEED_SELECTION_NAMED 2

#define IPRANGE_V4_ABI1_ALGEBRA_SET_UNION 1

#define IPRANGE_V4_ABI1_ALGEBRA_SET_INTERSECTION 2

#define IPRANGE_V4_ABI1_ALGEBRA_SET_EXCLUSION 3

#define IPRANGE_V4_ABI1_ALGEBRA_OUTPUT_PRESERVE_FEEDS 1

#define IPRANGE_V4_ABI1_ALGEBRA_OUTPUT_FLAT 2

#define IPRANGE_V4_ABI1_VALIDATION_MODE_LIVE_CURRENT 1

#define IPRANGE_V4_ABI1_VALIDATION_MODE_IMMUTABLE_CURRENT 2

#define IPRANGE_V4_ABI1_VALIDATION_MODE_OFFLINE_CANDIDATE 3

#define IPRANGE_V4_ABI1_RECOVERY_CANDIDATE_NEWEST 1

#define IPRANGE_V4_ABI1_RECOVERY_CANDIDATE_PREVIOUS 2

#define IPRANGE_V4_ABI1_RECOVERY_CANDIDATE_UNORDERED_META0 3

#define IPRANGE_V4_ABI1_RECOVERY_CANDIDATE_UNORDERED_META1 4

#define IPRANGE_V4_ABI1_CLEANUP_STATE_CLEAN 1

#define IPRANGE_V4_ABI1_CLEANUP_STATE_RESIDUE_POSSIBLE 2

#define IPRANGE_V4_ABI1_COORDINATION_CLEANUP_NONE 1

#define IPRANGE_V4_ABI1_COORDINATION_CLEANUP_GUARD 2

#define IPRANGE_V4_ABI1_COORDINATION_CLEANUP_RETAINED_READER_CLOSE_REQUIRED 3

#define IPRANGE_V4_ABI1_COORDINATION_CLEANUP_RETAINED_WRITER_CLOSE_REQUIRED 4

#define IPRANGE_V4_ABI1_HOUSEKEEPING_NONE 1

#define IPRANGE_V4_ABI1_HOUSEKEEPING_CRASH_REAPPEARANCE_POSSIBLE 2

#define IPRANGE_V4_ABI1_HOUSEKEEPING_VISIBLE 3

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_TRANSITION_MOVE_PENDING 1

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_TRANSITION_MOVE_AMBIGUOUS 2

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_TRANSITION_INERT 3

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_TRANSITION_CONFLICT 4

#define IPRANGE_V4_ABI1_ACCESS_ABSENT 1

#define IPRANGE_V4_ABI1_ACCESS_CREATOR_ONLY 2

#define IPRANGE_V4_ABI1_ACCESS_CHANGED_OR_UNPROVEN 3

#define IPRANGE_V4_ABI1_ACCESS_UNCLASSIFIED 4

#define IPRANGE_V4_ABI1_META_SELECTION_PROVEN_CURRENT 1

#define IPRANGE_V4_ABI1_META_SELECTION_SOLE_META0 2

#define IPRANGE_V4_ABI1_META_SELECTION_SOLE_META1 3

#define IPRANGE_V4_ABI1_COMMIT_DURABILITY_NOT_COMMITTED 1

#define IPRANGE_V4_ABI1_COMMIT_DURABILITY_COMMITTED 2

#define IPRANGE_V4_ABI1_COMMIT_DURABILITY_OUTCOME_UNKNOWN 3

#define IPRANGE_V4_ABI1_CREATION_NOT_CREATED 1

#define IPRANGE_V4_ABI1_CREATION_CREATED 2

#define IPRANGE_V4_ABI1_CREATION_OUTCOME_UNKNOWN 3

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_NOT_INITIALIZED 1

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_OLD_COORDINATION_RETAINED 2

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_LEFT_IMMUTABLE 3

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_INITIALIZED 4

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_OUTCOME_UNKNOWN 5

#define IPRANGE_V4_ABI1_PUBLICATION_NOT_PUBLISHED 1

#define IPRANGE_V4_ABI1_PUBLICATION_PUBLISHED 2

#define IPRANGE_V4_ABI1_PUBLICATION_OUTCOME_UNKNOWN 3

#define IPRANGE_V4_ABI1_DESTINATION_CONTENT_DESIRED 1

#define IPRANGE_V4_ABI1_DESTINATION_CONTENT_PREVIOUS 2

#define IPRANGE_V4_ABI1_DESTINATION_CONTENT_ABSENT 3

#define IPRANGE_V4_ABI1_DESTINATION_CONTENT_OTHER 4

#define IPRANGE_V4_ABI1_DESTINATION_CONTENT_UNCLASSIFIED 5

#define IPRANGE_V4_ABI1_LATER_CANONICAL_OWNER_NONE 1

#define IPRANGE_V4_ABI1_LATER_CANONICAL_OWNER_RESERVATION_OR_TRANSITION 2

#define IPRANGE_V4_ABI1_LATER_CANONICAL_OWNER_READY_LIVE_SIDECAR 3

#define IPRANGE_V4_ABI1_LIVE_LINEAGE_SAME_GENERATION_EXACT_BYTES 1

#define IPRANGE_V4_ABI1_LIVE_LINEAGE_SAME_GENERATION_PHYSICAL_BYTES_CHANGED 2

#define IPRANGE_V4_ABI1_LIVE_LINEAGE_ADVANCED_GENERATION 3

#define IPRANGE_V4_ABI1_LOCAL_FILE_RELATION_SAME_LOCAL_FILE 1

#define IPRANGE_V4_ABI1_LOCAL_FILE_RELATION_DIFFERENT_LOCAL_FILE 2

#define IPRANGE_V4_ABI1_COMMIT_RESOLUTION_COMMITTED 1

#define IPRANGE_V4_ABI1_COMMIT_RESOLUTION_NOT_COMMITTED 2

#define IPRANGE_V4_ABI1_COMMIT_RESOLUTION_SUPERSEDED_UNKNOWN 3

#define IPRANGE_V4_ABI1_COMMIT_RESOLUTION_UNRESOLVABLE 4

#define IPRANGE_V4_ABI1_ABORT_OUTCOME_ABORTED 1

#define IPRANGE_V4_ABI1_ABORT_OUTCOME_ABORT_INCOMPLETE 2

#define IPRANGE_V4_ABI1_CLOSE_OUTCOME_CLOSED 1

#define IPRANGE_V4_ABI1_CLOSE_OUTCOME_INCOMPLETE 2

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_OPERATION_INITIALIZE 1

#define IPRANGE_V4_ABI1_LIVE_TRANSITION_OPERATION_RESET 2

#define IPRANGE_V4_ABI1_LIVE_COORDINATION_LOCATION_ABSENT 1

#define IPRANGE_V4_ABI1_LIVE_COORDINATION_LOCATION_CANONICAL 2

#define IPRANGE_V4_ABI1_LIVE_COORDINATION_LOCATION_PRIVATE 3

#define IPRANGE_V4_ABI1_LIVE_COORDINATION_LOCATION_UNCLASSIFIED 4

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_STATUS_ABSENT 1

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_STATUS_READY 2

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_STATUS_COMPLETED 3

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_STATUS_REMOVED 4

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_STATUS_OUTCOME_UNKNOWN 5

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_KIND_CANONICAL 1

#define IPRANGE_V4_ABI1_LIVE_RESIDUE_KIND_PRIVATE_RESET 2

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_PRIVATE_OUTPUT 1

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_PRIVATE_RESERVATION 2

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_OWNED_COORDINATION 3

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_AUTHORIZED_SCRATCH 4

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_OWNED_MAIN 5

#define IPRANGE_V4_ABI1_ARTIFACT_KIND_UNPUBLISHED_MAIN_TAIL 6

#define IPRANGE_V4_ABI1_ARTIFACT_PRESENCE_ABSENT 1

#define IPRANGE_V4_ABI1_ARTIFACT_PRESENCE_PRESENT 2

#define IPRANGE_V4_ABI1_ARTIFACT_PRESENCE_UNCLASSIFIED 3

#define IPRANGE_V4_ABI1_ARTIFACT_RECORD_KIND_AUTHORIZED_SCRATCH 1

#define IPRANGE_V4_ABI1_ARTIFACT_RECORD_KIND_PUBLICATION_TEMP 2

#define IPRANGE_V4_ABI1_ARTIFACT_RECORD_KIND_PUBLICATION_RESERVATION 3

#define IPRANGE_V4_ABI1_SCRATCH_AUTHENTICATION_UNAUTHENTICATED 0

#define IPRANGE_V4_ABI1_SCRATCH_AUTHENTICATION_VALIDATION 1

#define IPRANGE_V4_ABI1_SCRATCH_AUTHENTICATION_RECOVERY 2

#define IPRANGE_V4_ABI1_ABANDONED_RESERVATION_PHASE_PREPARED 1

#define IPRANGE_V4_ABI1_ABANDONED_RESERVATION_PHASE_MAIN_MAY_HAVE_BEEN_ATTEMPTED 2

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_CANDIDATE_ENVELOPE 1

#define IPRANGE_V4_ABI1_WINDOWS_HOUSEKEEPING_CANDIDATE_INERT_PAYLOAD 2

#define IPRANGE_V4_ABI1_DIRECTORY_ROLE_DESTINATION 1

#define IPRANGE_V4_ABI1_DIRECTORY_ROLE_SCRATCH_DIRECTORY 2

#define IPRANGE_V4_ABI1_DIRECTORY_ROLE_MAIN_FILE 3

#define IPRANGE_V4_ABI1_LOCAL_IDENTITY_KIND_POSIX 1

#define IPRANGE_V4_ABI1_LOCAL_IDENTITY_KIND_WINDOWS 2

#define IPRANGE_V4_ABI1_CREATION_SECURITY_KIND_POSIX 1

#define IPRANGE_V4_ABI1_CREATION_SECURITY_KIND_WINDOWS 2

#define IPRANGE_V4_ABI1_OBJECT_KIND_FILE_GEOMETRY 1

#define IPRANGE_V4_ABI1_OBJECT_KIND_META 2

#define IPRANGE_V4_ABI1_OBJECT_KIND_RANGE_TREE 3

#define IPRANGE_V4_ABI1_OBJECT_KIND_CATALOG_NAME_TREE 4

#define IPRANGE_V4_ABI1_OBJECT_KIND_CATALOG_INDEX_TREE 5

#define IPRANGE_V4_ABI1_OBJECT_KIND_MEMBERSHIP_DICTIONARY 6

#define IPRANGE_V4_ABI1_OBJECT_KIND_MEMBERSHIP_REVERSE_INDEX 7

#define IPRANGE_V4_ABI1_OBJECT_KIND_MEMBERSHIP_BLOB 8

#define IPRANGE_V4_ABI1_OBJECT_KIND_METADATA 9

#define IPRANGE_V4_ABI1_OBJECT_KIND_FREE_BITMAP 10

#define IPRANGE_V4_ABI1_OBJECT_KIND_FEED_USED_BITMAP 11

#define IPRANGE_V4_ABI1_OBJECT_KIND_MEMBERSHIP_USED_BITMAP 12

#define IPRANGE_V4_ABI1_OBJECT_KIND_RETIREMENT_TREE 13

#define IPRANGE_V4_ABI1_OBJECT_KIND_RETIREMENT_BLOB 14

#define IPRANGE_V4_ABI1_OBJECT_KIND_STRUCTURE_DICTIONARY 15

#define IPRANGE_V4_ABI1_OBJECT_KIND_STRUCTURE_REVERSE_INDEX 16

#define IPRANGE_V4_ABI1_OBJECT_KIND_STRUCTURE_USED_BITMAP 17

#define IPRANGE_V4_ABI1_LOGICAL_CHANGE_CHANGED 1

#define IPRANGE_V4_ABI1_LOGICAL_CHANGE_NO_CHANGE 2

#define IPRANGE_V4_ABI1_DIRECT_SEMANTIC_NOT_APPLICABLE 0

#define IPRANGE_V4_ABI1_DIRECT_SEMANTIC_GENERIC 1

#define IPRANGE_V4_ABI1_DIRECT_SEMANTIC_FIRST_SEEN 2

#define IPRANGE_V4_ABI1_DIRECT_SEMANTIC_LAST_SEEN 3

#define IPRANGE_V4_ABI1_WORKFLOW_CREATE_FEED 1

#define IPRANGE_V4_ABI1_WORKFLOW_REPLACE_FEED 2

#define IPRANGE_V4_ABI1_WORKFLOW_DIRECT_REPLACEMENT 3

#define IPRANGE_V4_ABI1_WORKFLOW_FIRST_SEEN_REFRESH 4

#define IPRANGE_V4_ABI1_WORKFLOW_LAST_SEEN_REFRESH 5

#define IPRANGE_V4_ABI1_WORKFLOW_MEMBERSHIP_IMPORT 6

#define IPRANGE_V4_ABI1_REPORT_KIND_SCAN 1

#define IPRANGE_V4_ABI1_REPORT_KIND_FINISH_INPUT 2

#define IPRANGE_V4_ABI1_REPORT_KIND_COMMIT 3

#define IPRANGE_V4_ABI1_REPORT_KIND_COMMIT_RESOLUTION 4

#define IPRANGE_V4_ABI1_REPORT_KIND_ABORT 5

#define IPRANGE_V4_ABI1_REPORT_KIND_CLOSE 6

#define IPRANGE_V4_ABI1_REPORT_KIND_RECLAIM 7

#define IPRANGE_V4_ABI1_REPORT_KIND_CREATE 8

#define IPRANGE_V4_ABI1_REPORT_KIND_LIVE_TRANSITION 9

#define IPRANGE_V4_ABI1_REPORT_KIND_CREATE_RESOLUTION 10

#define IPRANGE_V4_ABI1_REPORT_KIND_LIVE_TRANSITION_RESOLUTION 11

#define IPRANGE_V4_ABI1_REPORT_KIND_PUBLICATION 12

#define IPRANGE_V4_ABI1_REPORT_KIND_VALIDATION 13

#define IPRANGE_V4_ABI1_REPORT_KIND_RECOVERY_CANDIDATES 14

#define IPRANGE_V4_ABI1_REPORT_KIND_RECOVERY 15

#define IPRANGE_V4_ABI1_REPORT_KIND_RESIDUE 16

#define IPRANGE_V4_ABI1_REPORT_KIND_LIVE_RESIDUE 17

#define IPRANGE_V4_ABI1_REPORT_KIND_HISTORY_PROJECTION 18

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_INSPECT_PUBLICATION 1

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_PUBLICATION 2

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH 3

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_SCRATCH 4

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS 5

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_PUBLICATION_TEMP 6

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_RESERVATION_ARTIFACTS 7

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_RESERVATION_ARTIFACT 8

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_HOUSEKEEPING_ARTIFACTS 9

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_HOUSEKEEPING_ARTIFACT 10

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_SNAPSHOT_PREPARATION_FAILURE 11

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_IMMUTABLE_FEED_PREPARATION_FAILURE 12

#define IPRANGE_V4_ABI1_RESIDUE_OPERATION_ALGEBRA_PREPARATION_FAILURE 13

#define IPRANGE_V4_ABI1_RESIDUE_COORDINATION_ABSENT 1

#define IPRANGE_V4_ABI1_RESIDUE_COORDINATION_PUBLICATION_RESERVATION 2

#define IPRANGE_V4_ABI1_RESIDUE_COORDINATION_LIVE_SIDECAR 3

#define IPRANGE_V4_ABI1_RESIDUE_COORDINATION_UNSELECTABLE 4

#define IPRANGE_V4_ABI1_RESIDUE_MAIN_CONTENT_V4 1

#define IPRANGE_V4_ABI1_RESIDUE_MAIN_CONTENT_OTHER 2

#define IPRANGE_V4_ABI1_REASON_META_UNAVAILABLE 1

#define IPRANGE_V4_ABI1_REASON_META_INVALID 2

#define IPRANGE_V4_ABI1_REASON_META_STATIC_MISMATCH 3

#define IPRANGE_V4_ABI1_REASON_FILE_GEOMETRY_INVALID 4

#define IPRANGE_V4_ABI1_REASON_ROOT_COUNT_INVALID 5

#define IPRANGE_V4_ABI1_REASON_IO_ERROR 6

#define IPRANGE_V4_ABI1_REASON_ARITHMETIC_OVERFLOW 7

#define IPRANGE_V4_ABI1_REASON_PAGE_OUT_OF_BOUNDS 8

#define IPRANGE_V4_ABI1_REASON_PAGE_HEADER_INVALID 9

#define IPRANGE_V4_ABI1_REASON_PAGE_CRC_MISMATCH 10

#define IPRANGE_V4_ABI1_REASON_PAGE_TYPE_MISMATCH 11

#define IPRANGE_V4_ABI1_REASON_PAGE_BORN_TXN_INVALID 12

#define IPRANGE_V4_ABI1_REASON_PAGE_RESERVED_NONZERO 13

#define IPRANGE_V4_ABI1_REASON_TREE_CYCLE 14

#define IPRANGE_V4_ABI1_REASON_PAGE_ALIAS 15

#define IPRANGE_V4_ABI1_REASON_TREE_LEVEL_INVALID 16

#define IPRANGE_V4_ABI1_REASON_TREE_ORDER_INVALID 17

#define IPRANGE_V4_ABI1_REASON_TREE_FENCE_INVALID 18

#define IPRANGE_V4_ABI1_REASON_RANGE_REVERSED 19

#define IPRANGE_V4_ABI1_REASON_RANGE_OVERLAP 20

#define IPRANGE_V4_ABI1_REASON_RANGE_NOT_COALESCED 21

#define IPRANGE_V4_ABI1_REASON_CATALOG_NAME_INVALID 22

#define IPRANGE_V4_ABI1_REASON_CATALOG_BIJECTION_INVALID 23

#define IPRANGE_V4_ABI1_REASON_CATALOG_BITMAP_INVALID 24

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_BITMAP_INVALID 25

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_HASH_INVALID 26

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_REVERSE_INDEX_INVALID 27

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_REFCOUNT_INVALID 28

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_ACTIVE_FEED_INVALID 29

#define IPRANGE_V4_ABI1_REASON_BLOB_INVALID 30

#define IPRANGE_V4_ABI1_REASON_METADATA_ZLIB_INVALID 31

#define IPRANGE_V4_ABI1_REASON_METADATA_LENGTH_INVALID 32

#define IPRANGE_V4_ABI1_REASON_BITMAP_SUMMARY_INVALID 33

#define IPRANGE_V4_ABI1_REASON_ALLOCATION_PARTITION_INVALID 34

#define IPRANGE_V4_ABI1_REASON_RETIREMENT_ORDER_INVALID 35

#define IPRANGE_V4_ABI1_REASON_RETIREMENT_LIST_INVALID 36

#define IPRANGE_V4_ABI1_REASON_CATALOG_INVALID 37

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_MISSING 38

#define IPRANGE_V4_ABI1_REASON_MEMBERSHIP_INVALID 39

#define IPRANGE_V4_ABI1_REASON_METADATA_INVALID 40

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_PAYLOAD_INVALID 41

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_HASH_INVALID 42

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_REVERSE_INDEX_INVALID 43

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_REFCOUNT_INVALID 44

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_MEMBERSHIP_INVALID 45

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_MISSING 46

#define IPRANGE_V4_ABI1_REASON_STRUCTURE_INVALID 47

#define IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ARGUMENT 1

#define IPRANGE_V4_ABI1_ERROR_CODE_NULL_POINTER 2

#define IPRANGE_V4_ABI1_ERROR_CODE_MISALIGNED_POINTER 3

#define IPRANGE_V4_ABI1_ERROR_CODE_INVALID_LENGTH 4

#define IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ENUM 5

#define IPRANGE_V4_ABI1_ERROR_CODE_RESERVED_NONZERO 6

#define IPRANGE_V4_ABI1_ERROR_CODE_BUFFER_TOO_SMALL 7

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_HANDLE_KIND 8

#define IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_CLOSED 9

#define IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_BUSY 10

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_STATE 11

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_ADDRESS_FAMILY 12

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_VALUE_KIND 13

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_VALUE_TAG 14

#define IPRANGE_V4_ABI1_ERROR_CODE_RANGE_REVERSED 15

#define IPRANGE_V4_ABI1_ERROR_CODE_NAME_INVALID 16

#define IPRANGE_V4_ABI1_ERROR_CODE_NAME_EXISTS 17

#define IPRANGE_V4_ABI1_ERROR_CODE_NAME_NOT_FOUND 18

#define IPRANGE_V4_ABI1_ERROR_CODE_STALE_REFERENCE 19

#define IPRANGE_V4_ABI1_ERROR_CODE_FOREIGN_REFERENCE 20

#define IPRANGE_V4_ABI1_ERROR_CODE_NO_PENDING_TRANSACTION 21

#define IPRANGE_V4_ABI1_ERROR_CODE_TRANSACTION_ABORTED 22

#define IPRANGE_V4_ABI1_ERROR_CODE_ABORT_INCOMPLETE 23

#define IPRANGE_V4_ABI1_ERROR_CODE_INSUFFICIENT_RESOURCE_BUDGET 24

#define IPRANGE_V4_ABI1_ERROR_CODE_PAGE_SPACE_EXHAUSTED 25

#define IPRANGE_V4_ABI1_ERROR_CODE_WORK_LIMIT_TOO_SMALL 26

#define IPRANGE_V4_ABI1_ERROR_CODE_CANCELLED 27

#define IPRANGE_V4_ABI1_ERROR_CODE_SOURCE_FAILED 28

#define IPRANGE_V4_ABI1_ERROR_CODE_SINK_FAILED 29

#define IPRANGE_V4_ABI1_ERROR_CODE_STOPPED_BY_SINK 30

#define IPRANGE_V4_ABI1_ERROR_CODE_IO 31

#define IPRANGE_V4_ABI1_ERROR_CODE_FORMAT_INVALID 32

#define IPRANGE_V4_ABI1_ERROR_CODE_NOT_V4 33

#define IPRANGE_V4_ABI1_ERROR_CODE_DURABILITY_UNSUPPORTED 34

#define IPRANGE_V4_ABI1_ERROR_CODE_PUBLICATION_UNSUPPORTED 35

#define IPRANGE_V4_ABI1_ERROR_CODE_ACCESS_POLICY_UNSUPPORTED 36

#define IPRANGE_V4_ABI1_ERROR_CODE_CONFLICT 37

#define IPRANGE_V4_ABI1_ERROR_CODE_UNRESOLVABLE 38

#define IPRANGE_V4_ABI1_ERROR_CODE_WRITER_BUSY 39

#define IPRANGE_V4_ABI1_ERROR_CODE_DIRECTORY_IDENTITY_MISMATCH 40

#define IPRANGE_V4_ABI1_ERROR_CODE_DESTINATION_NAME_MISMATCH 41

#define IPRANGE_V4_ABI1_ERROR_CODE_CLEANUP_CONFLICT 42

#define IPRANGE_V4_ABI1_ERROR_CODE_COORDINATION_SEQUENCE_EXHAUSTED 43

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_COORDINATION_UNSUPPORTED 44

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_COORDINATION_CLEANUP_REQUIRED 45

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_COORDINATION_MALFORMED_REQUIRES_RESET 46

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_OPEN_CLEANUP_REQUIRED 47

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_RECOVERY_COORDINATION_UNAVAILABLE 48

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_RECOVERY_CURRENT_GENERATION_UNPROVABLE 49

#define IPRANGE_V4_ABI1_ERROR_CODE_LIVE_RECOVERY_CURRENT_GENERATION_UNREADABLE 50

#define IPRANGE_V4_ABI1_ERROR_CODE_RECOVERY_CANDIDATE_CHANGED 51

#define IPRANGE_V4_ABI1_ERROR_CODE_RECOVERY_PREPARATION_FAILED 52

#define IPRANGE_V4_ABI1_ERROR_CODE_SNAPSHOT_PREPARATION_FAILED 53

#define IPRANGE_V4_ABI1_ERROR_CODE_TRANSITION_SUPERSEDED 54

#define IPRANGE_V4_ABI1_ERROR_CODE_CURRENT_GENERATION_UNPROVABLE 55

#define IPRANGE_V4_ABI1_ERROR_CODE_FORKED_HANDLE 56

#define IPRANGE_V4_ABI1_ERROR_CODE_PANIC 57

#define IPRANGE_V4_ABI1_ERROR_CODE_OS_UNSUPPORTED 58

#define IPRANGE_V4_ABI1_ERROR_CODE_TRANSACTION_ID_EXHAUSTED 59

#define IPRANGE_V4_ABI1_ERROR_CODE_ARITHMETIC_OVERFLOW 60

#define IPRANGE_V4_ABI1_ERROR_CODE_FEED_INDEX_EXHAUSTED 61

#define IPRANGE_V4_ABI1_ERROR_CODE_MEMBERSHIP_ID_EXHAUSTED 62

#define IPRANGE_V4_ABI1_ERROR_CODE_READER_CAPACITY_EXHAUSTED 63

#define IPRANGE_V4_ABI1_ERROR_CODE_CLEANUP_IN_PROGRESS 64

#define IPRANGE_V4_ABI1_ERROR_CODE_FAULT_WORKER_UNAVAILABLE 65

#define IPRANGE_V4_ABI1_ERROR_CODE_FAULT_WORKER_FAILED 66

#define IPRANGE_V4_ABI1_ERROR_CODE_UNSUPPORTED_STRUCTURE 67

#define IPRANGE_V4_ABI1_ERROR_CODE_WRONG_STRUCTURE_KIND 68

#define IPRANGE_V4_ABI1_ERROR_CODE_STRUCTURE_ID_EXHAUSTED 69

typedef struct iprange_v4_abi1_membership_algebra_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint32_t max_sources;
    uint32_t reserved;
} iprange_v4_abi1_membership_algebra_budget;

typedef uint8_t (*iprange_v4_abi1_cancel_fn)(void *context);

typedef struct iprange_v4_abi1_cancellation {
    iprange_v4_abi1_cancel_fn callback;
    void *context;
} iprange_v4_abi1_cancellation;

typedef struct iprange_v4_abi1_feed_name_value {
    uint32_t length;
    uint8_t bytes[255];
    uint8_t reserved;
} iprange_v4_abi1_feed_name_value;

typedef struct iprange_v4_abi1_callback_failure {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t caller_code;
    const uint8_t *message_pointer;
    uint64_t message_length;
} iprange_v4_abi1_callback_failure;

typedef uint32_t (*iprange_v4_abi1_feed_name_sink_fn)(void *context,
                                                      const struct iprange_v4_abi1_feed_name_value *records,
                                                      uint64_t count,
                                                      struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_byte_slice {
    const uint8_t *pointer;
    uint64_t length;
} iprange_v4_abi1_byte_slice;

typedef struct iprange_v4_abi1_feed_selection_input {
    uint32_t kind;
    uint32_t reserved;
    const struct iprange_v4_abi1_byte_slice *names;
    uint64_t name_count;
} iprange_v4_abi1_feed_selection_input;

typedef struct iprange_v4_abi1_cardinality129 {
    uint8_t bit128;
    uint8_t reserved[7];
    uint64_t hi;
    uint64_t lo;
} iprange_v4_abi1_cardinality129;

typedef struct iprange_v4_abi1_algebra_count_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t source_count;
    uint64_t source_range_count;
    uint64_t joined_segment_count;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_algebra_count_report;

typedef struct iprange_v4_abi1_algebra_comparison_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t source_count;
    uint64_t source_range_count;
    uint64_t joined_segment_count;
    struct iprange_v4_abi1_cardinality129 left_addresses;
    struct iprange_v4_abi1_cardinality129 right_addresses;
    struct iprange_v4_abi1_cardinality129 overlap_addresses;
    struct iprange_v4_abi1_cardinality129 left_only_addresses;
    struct iprange_v4_abi1_cardinality129 right_only_addresses;
    struct iprange_v4_abi1_cardinality129 union_addresses;
    uint32_t equal;
    uint32_t reserved;
} iprange_v4_abi1_algebra_comparison_report;

typedef struct iprange_v4_abi1_path {
    uint32_t kind;
    uint32_t reserved;
    const void *pointer;
    uint64_t length;
} iprange_v4_abi1_path;

typedef struct iprange_v4_abi1_algebra_set_operation_input {
    uint32_t kind;
    uint32_t reserved;
    struct iprange_v4_abi1_feed_selection_input included;
    struct iprange_v4_abi1_feed_selection_input excluded;
} iprange_v4_abi1_algebra_set_operation_input;

typedef struct iprange_v4_abi1_algebra_output_mode_input {
    uint32_t kind;
    uint32_t reserved;
    struct iprange_v4_abi1_byte_slice flat_name;
} iprange_v4_abi1_algebra_output_mode_input;

typedef struct iprange_v4_abi1_optional_byte_slice {
    uint8_t present;
    uint8_t reserved[7];
    struct iprange_v4_abi1_byte_slice value;
} iprange_v4_abi1_optional_byte_slice;

typedef struct iprange_v4_abi1_algebra_output_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_output_pages;
    uint32_t max_open_files;
    uint32_t reserved;
} iprange_v4_abi1_algebra_output_budget;

typedef struct iprange_v4_abi1_algebra_set_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t source_count;
    uint64_t source_range_count;
    uint64_t joined_segment_count;
    uint64_t output_feed_count;
    uint64_t output_range_count;
    struct iprange_v4_abi1_cardinality129 output_addresses;
} iprange_v4_abi1_algebra_set_report;

typedef struct iprange_v4_abi1_ip {
    uint32_t family;
    uint8_t bytes[16];
} iprange_v4_abi1_ip;

typedef struct iprange_v4_abi1_range {
    struct iprange_v4_abi1_ip from;
    struct iprange_v4_abi1_ip to;
} iprange_v4_abi1_range;

typedef struct iprange_v4_abi1_direct_range {
    struct iprange_v4_abi1_range range;
    uint32_t value;
    uint32_t reserved;
} iprange_v4_abi1_direct_range;

typedef struct iprange_v4_abi1_membership_range {
    struct iprange_v4_abi1_range range;
    const iprange_v4_abi1_borrowed_membership_view *membership;
} iprange_v4_abi1_membership_range;

typedef struct iprange_v4_abi1_network_enrichment_v1 {
    uint32_t asn;
    uint32_t country_id;
    uint32_t state_id;
    uint32_t city_id;
    int32_t latitude_microdegrees;
    int32_t longitude_microdegrees;
    uint32_t has_location;
    uint32_t reserved;
} iprange_v4_abi1_network_enrichment_v1;

typedef struct iprange_v4_abi1_network_enrichment_v1_range {
    struct iprange_v4_abi1_range range;
    struct iprange_v4_abi1_network_enrichment_v1 value;
    const iprange_v4_abi1_borrowed_membership_view *membership;
} iprange_v4_abi1_network_enrichment_v1_range;

typedef struct iprange_v4_abi1_mutable_byte_slice {
    uint8_t *pointer;
    uint64_t length;
} iprange_v4_abi1_mutable_byte_slice;

typedef struct iprange_v4_abi1_local_identity {
    uint32_t kind;
    uint32_t reserved;
    uint8_t bytes[32];
} iprange_v4_abi1_local_identity;

typedef struct iprange_v4_abi1_local_basename {
    uint32_t encoding;
    uint32_t length;
    uint8_t bytes[512];
} iprange_v4_abi1_local_basename;

typedef struct iprange_v4_abi1_cleanup_artifact {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t kind;
    uint32_t directory_role;
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_local_basename basename;
    uint8_t artifact_identity_present;
    uint8_t creation_security_present;
    uint8_t reserved0[6];
    struct iprange_v4_abi1_local_identity artifact_identity;
    uint32_t creation_security_kind;
    uint32_t reserved1;
    uint8_t creation_security_commitment[32];
    uint8_t unpublished_tail_present;
    uint8_t reserved2[7];
    uint8_t expected_database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
    uint64_t expected_length;
    uint64_t observed_end_exclusive;
    uint32_t error_code;
    uint8_t error_os_code_present;
    uint8_t reserved3[3];
    int32_t error_os_code;
    uint32_t reserved4;
} iprange_v4_abi1_cleanup_artifact;

typedef struct iprange_v4_abi1_history_window_input {
    struct iprange_v4_abi1_byte_slice feed_name;
    uint32_t cutoff;
    uint32_t reserved;
} iprange_v4_abi1_history_window_input;

typedef uint32_t (*iprange_v4_abi1_coverage_source_fn)(void *context,
                                                       struct iprange_v4_abi1_range *records,
                                                       uint64_t capacity,
                                                       uint64_t *count,
                                                       struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_immutable_feed_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint64_t max_output_pages;
    uint64_t max_workspace_pages;
    uint32_t max_open_files;
    uint32_t reserved;
} iprange_v4_abi1_immutable_feed_budget;

typedef struct iprange_v4_abi1_immutable_feed_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t input_record_count;
    uint64_t normalized_interval_count;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_immutable_feed_report;

typedef struct iprange_v4_abi1_transaction_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint64_t max_private_pages;
    uint64_t max_file_growth_pages;
    uint32_t max_open_files;
    uint32_t reserved;
} iprange_v4_abi1_transaction_budget;

typedef struct iprange_v4_abi1_optional_identity {
    uint8_t present;
    uint8_t reserved[7];
    struct iprange_v4_abi1_local_identity value;
} iprange_v4_abi1_optional_identity;

typedef struct iprange_v4_abi1_publication_tuple {
    uint8_t database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
} iprange_v4_abi1_publication_tuple;

typedef struct iprange_v4_abi1_publication_digest {
    uint64_t byte_length;
    uint8_t sha512[64];
} iprange_v4_abi1_publication_digest;

typedef struct iprange_v4_abi1_artifact_record {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t record_kind;
    uint32_t authentication;
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_optional_identity artifact_identity;
    struct iprange_v4_abi1_optional_identity output_identity;
    uint8_t attempt_id[16];
    uint32_t ordinal;
    uint32_t policy;
    uint32_t phase;
    uint32_t reserved;
    uint8_t tuple_present;
    uint8_t digest_present;
    uint8_t previous_present;
    uint8_t reserved1[5];
    struct iprange_v4_abi1_publication_tuple tuple;
    struct iprange_v4_abi1_publication_digest digest;
    struct iprange_v4_abi1_local_identity previous_identity;
    struct iprange_v4_abi1_publication_digest previous_digest;
    struct iprange_v4_abi1_local_basename basename;
} iprange_v4_abi1_artifact_record;

typedef uint32_t (*iprange_v4_abi1_artifact_sink_fn)(void *context,
                                                     const struct iprange_v4_abi1_artifact_record *records,
                                                     uint64_t count,
                                                     struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_optional_bytes16 {
    uint8_t present;
    uint8_t reserved[7];
    uint8_t value[16];
} iprange_v4_abi1_optional_bytes16;

typedef struct iprange_v4_abi1_optional_u32 {
    uint8_t present;
    uint8_t reserved[3];
    uint32_t value;
} iprange_v4_abi1_optional_u32;

typedef struct iprange_v4_abi1_creation_security {
    uint8_t present;
    uint8_t reserved0[3];
    uint32_t kind;
    uint8_t commitment[32];
} iprange_v4_abi1_creation_security;

typedef struct iprange_v4_abi1_housekeeping_artifact {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t state;
    uint32_t directory_role;
    struct iprange_v4_abi1_local_identity directory_identity;
    uint32_t basename_encoding;
    uint32_t ordinal;
    uint8_t attempt_id[16];
    struct iprange_v4_abi1_local_basename envelope_basename;
    struct iprange_v4_abi1_local_identity envelope_identity;
    struct iprange_v4_abi1_local_basename source_basename;
    struct iprange_v4_abi1_local_basename inert_basename;
    uint32_t source_presence;
    uint32_t inert_presence;
    struct iprange_v4_abi1_optional_identity source_identity;
    struct iprange_v4_abi1_optional_identity inert_identity;
    uint32_t kind;
    struct iprange_v4_abi1_creation_security creation_security;
    uint64_t selected_envelope_sequence;
} iprange_v4_abi1_housekeeping_artifact;

typedef struct iprange_v4_abi1_housekeeping_record {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t candidate_kind;
    struct iprange_v4_abi1_local_basename basename;
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_optional_identity identity;
    struct iprange_v4_abi1_optional_bytes16 attempt_id;
    struct iprange_v4_abi1_optional_u32 ordinal;
    uint8_t artifact_present;
    uint8_t problem_present;
    uint8_t reserved[6];
    struct iprange_v4_abi1_housekeeping_artifact artifact;
    uint32_t problem_code;
    uint8_t problem_os_code_present;
    uint8_t reserved1[3];
    int32_t problem_os_code;
    uint32_t reserved2;
} iprange_v4_abi1_housekeeping_record;

typedef uint32_t (*iprange_v4_abi1_housekeeping_sink_fn)(void *context,
                                                         const struct iprange_v4_abi1_housekeeping_record *records,
                                                         uint64_t count,
                                                         struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_housekeeping_payload {
    uint8_t tuple_present;
    uint8_t reserved[7];
    struct iprange_v4_abi1_publication_tuple tuple;
    struct iprange_v4_abi1_publication_digest digest;
} iprange_v4_abi1_housekeeping_payload;

typedef struct iprange_v4_abi1_feed_info {
    uint32_t index;
    uint32_t name_length;
    uint8_t name[255];
    uint8_t reserved;
} iprange_v4_abi1_feed_info;

typedef uint32_t (*iprange_v4_abi1_feed_sink_fn)(void *context,
                                                 const struct iprange_v4_abi1_feed_info *records,
                                                 uint64_t count,
                                                 struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_snapshot_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint64_t max_output_pages;
    uint32_t max_open_files;
    uint32_t reserved;
} iprange_v4_abi1_snapshot_budget;

typedef struct iprange_v4_abi1_membership_query_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
} iprange_v4_abi1_membership_query_budget;

typedef struct iprange_v4_abi1_matching_feeds_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t matching_feed_count;
} iprange_v4_abi1_matching_feeds_report;

typedef struct iprange_v4_abi1_feed_pair_input {
    struct iprange_v4_abi1_byte_slice left;
    struct iprange_v4_abi1_byte_slice right;
} iprange_v4_abi1_feed_pair_input;

typedef struct iprange_v4_abi1_feed_cardinality {
    struct iprange_v4_abi1_feed_name_value feed;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_feed_cardinality;

typedef uint32_t (*iprange_v4_abi1_feed_cardinality_sink_fn)(void *context,
                                                             const struct iprange_v4_abi1_feed_cardinality *records,
                                                             uint64_t count,
                                                             struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_feed_overlap {
    struct iprange_v4_abi1_feed_name_value left;
    struct iprange_v4_abi1_feed_name_value right;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_feed_overlap;

typedef uint32_t (*iprange_v4_abi1_feed_overlap_sink_fn)(void *context,
                                                         const struct iprange_v4_abi1_feed_overlap *records,
                                                         uint64_t count,
                                                         struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_membership_aggregation_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t scanned_range_count;
    struct iprange_v4_abi1_cardinality129 scanned_addresses;
    uint64_t feed_result_count;
    uint64_t pair_result_count;
} iprange_v4_abi1_membership_aggregation_report;

typedef struct iprange_v4_abi1_direct_join_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_result_cells;
} iprange_v4_abi1_direct_join_budget;

typedef struct iprange_v4_abi1_direct_join_cell {
    struct iprange_v4_abi1_feed_name_value feed;
    uint8_t direct_present;
    uint8_t reserved[3];
    uint32_t direct_value;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_direct_join_cell;

typedef uint32_t (*iprange_v4_abi1_direct_join_sink_fn)(void *context,
                                                        const struct iprange_v4_abi1_direct_join_cell *records,
                                                        uint64_t count,
                                                        struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_direct_join_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t membership_range_count;
    uint64_t direct_ranges_visited;
    uint64_t joined_segment_count;
    struct iprange_v4_abi1_cardinality129 selected_addresses;
    struct iprange_v4_abi1_cardinality129 mapped_addresses;
    struct iprange_v4_abi1_cardinality129 unmapped_addresses;
    uint64_t result_cell_count;
} iprange_v4_abi1_direct_join_report;

typedef struct iprange_v4_abi1_membership_cross_cell {
    struct iprange_v4_abi1_feed_name_value left;
    struct iprange_v4_abi1_feed_name_value right;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_membership_cross_cell;

typedef uint32_t (*iprange_v4_abi1_membership_cross_sink_fn)(void *context,
                                                             const struct iprange_v4_abi1_membership_cross_cell *records,
                                                             uint64_t count,
                                                             struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_uncovered_feed {
    uint32_t side;
    uint32_t reserved;
    struct iprange_v4_abi1_feed_name_value feed;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_uncovered_feed;

typedef uint32_t (*iprange_v4_abi1_uncovered_feed_sink_fn)(void *context,
                                                           const struct iprange_v4_abi1_uncovered_feed *records,
                                                           uint64_t count,
                                                           struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_membership_join_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t left_range_count;
    uint64_t right_range_count;
    uint64_t joined_segment_count;
    struct iprange_v4_abi1_cardinality129 left_addresses;
    struct iprange_v4_abi1_cardinality129 right_addresses;
    struct iprange_v4_abi1_cardinality129 overlap_addresses;
    struct iprange_v4_abi1_cardinality129 left_uncovered_addresses;
    struct iprange_v4_abi1_cardinality129 right_uncovered_addresses;
    uint64_t cross_result_count;
    uint64_t uncovered_result_count;
} iprange_v4_abi1_membership_join_report;

typedef struct iprange_v4_abi1_database_info {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t address_family;
    uint32_t value_kind;
    uint32_t direct_semantic;
    uint32_t structure_kind;
    uint8_t value_tag[16];
    uint8_t database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
    uint64_t page_count;
    uint64_t range_record_count;
    uint64_t active_feed_count;
    uint32_t meta_selection;
    uint32_t reserved2;
} iprange_v4_abi1_database_info;

typedef struct iprange_v4_abi1_scan_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t record_count;
    uint8_t completed;
    uint8_t reserved[7];
} iprange_v4_abi1_scan_report;

typedef struct iprange_v4_abi1_finish_input_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t workflow;
    uint32_t logical_change;
    uint64_t input_record_count;
    uint64_t input_normalized_interval_count;
    uint64_t before_range_record_count;
    uint64_t after_range_record_count;
    struct iprange_v4_abi1_cardinality129 input_addresses;
    struct iprange_v4_abi1_cardinality129 before_addresses;
    struct iprange_v4_abi1_cardinality129 after_addresses;
    struct iprange_v4_abi1_cardinality129 unchanged_value_addresses;
    struct iprange_v4_abi1_cardinality129 changed_value_addresses;
    struct iprange_v4_abi1_cardinality129 added_addresses;
    struct iprange_v4_abi1_cardinality129 removed_addresses;
    uint64_t source_feed_count;
    uint64_t matched_feed_count;
    uint64_t created_feed_count;
    uint64_t source_distinct_membership_count;
    uint64_t translated_membership_count;
} iprange_v4_abi1_finish_input_report;

typedef struct iprange_v4_abi1_history_projection_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t logical_change;
    uint32_t reserved;
    uint64_t source_range_count;
    struct iprange_v4_abi1_cardinality129 source_addresses;
    uint64_t created_feed_count;
    uint64_t before_interval_count;
    uint64_t after_interval_count;
    struct iprange_v4_abi1_cardinality129 before_addresses;
    struct iprange_v4_abi1_cardinality129 after_addresses;
    struct iprange_v4_abi1_cardinality129 unchanged_addresses;
    struct iprange_v4_abi1_cardinality129 added_addresses;
    struct iprange_v4_abi1_cardinality129 removed_addresses;
    uint64_t window_count;
} iprange_v4_abi1_history_projection_report;

typedef struct iprange_v4_abi1_history_window_report {
    struct iprange_v4_abi1_feed_name_value feed_name;
    uint32_t cutoff;
    uint8_t created;
    uint8_t reserved[3];
    uint64_t before_interval_count;
    uint64_t after_interval_count;
    struct iprange_v4_abi1_cardinality129 before_addresses;
    struct iprange_v4_abi1_cardinality129 after_addresses;
    struct iprange_v4_abi1_cardinality129 unchanged_addresses;
    struct iprange_v4_abi1_cardinality129 added_addresses;
    struct iprange_v4_abi1_cardinality129 removed_addresses;
} iprange_v4_abi1_history_window_report;

typedef struct iprange_v4_abi1_commit_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint8_t attempted_database_id[16];
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_local_identity main_identity;
    uint64_t attempted_transaction_id;
    uint8_t attempted_commit_nonce[16];
    uint32_t durability;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
    uint32_t reserved;
} iprange_v4_abi1_commit_report;

typedef struct iprange_v4_abi1_abort_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t outcome;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
    uint32_t reserved;
} iprange_v4_abi1_abort_report;

typedef struct iprange_v4_abi1_close_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t outcome;
    uint8_t abort_present;
    uint8_t reserved0[3];
    uint32_t abort_outcome;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
    uint32_t reserved1;
} iprange_v4_abi1_close_report;

typedef struct iprange_v4_abi1_reclaim_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint8_t changed;
    uint8_t reserved0[7];
    uint64_t transaction_count;
    uint64_t page_count;
    struct iprange_v4_abi1_commit_report commit;
} iprange_v4_abi1_reclaim_report;

typedef struct iprange_v4_abi1_commit_resolution_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint8_t attempted_database_id[16];
    uint64_t attempted_transaction_id;
    uint8_t attempted_commit_nonce[16];
    struct iprange_v4_abi1_local_identity actual_directory_identity;
    struct iprange_v4_abi1_local_identity actual_main_identity;
    uint32_t local_file_relation;
    uint32_t resolution;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
} iprange_v4_abi1_commit_resolution_report;

typedef struct iprange_v4_abi1_create_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t address_family;
    uint32_t value_kind;
    uint32_t structure_kind;
    uint8_t value_tag[16];
    uint8_t database_id[16];
    uint8_t commit_nonce[16];
    uint8_t sidecar_id[16];
    struct iprange_v4_abi1_optional_identity directory_identity;
    struct iprange_v4_abi1_local_basename main_basename;
    struct iprange_v4_abi1_optional_identity main_identity;
    struct iprange_v4_abi1_optional_identity sidecar_identity;
    uint32_t reader_capacity;
    uint32_t state;
    uint8_t residue_possible;
    uint8_t reserved[3];
    uint32_t housekeeping;
} iprange_v4_abi1_create_report;

typedef struct iprange_v4_abi1_live_transition_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t operation;
    uint32_t reset_policy;
    uint32_t status;
    uint32_t new_sidecar_location;
    uint8_t database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_local_identity main_identity;
    struct iprange_v4_abi1_local_basename main_basename;
    uint32_t reader_capacity;
    uint32_t housekeeping;
    uint8_t sidecar_id[16];
    struct iprange_v4_abi1_optional_identity previous_sidecar_identity;
    struct iprange_v4_abi1_optional_identity new_sidecar_identity;
    uint8_t residue_possible;
    uint8_t reserved[7];
} iprange_v4_abi1_live_transition_report;

typedef struct iprange_v4_abi1_live_residue_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t status;
    uint32_t kind;
    struct iprange_v4_abi1_optional_bytes16 database_id;
    struct iprange_v4_abi1_optional_bytes16 sidecar_id;
    struct iprange_v4_abi1_optional_u32 reader_capacity;
    struct iprange_v4_abi1_optional_identity main_identity;
    struct iprange_v4_abi1_optional_identity sidecar_identity;
    uint8_t residue_possible;
    uint8_t reserved[3];
    uint32_t housekeeping;
} iprange_v4_abi1_live_residue_report;

typedef struct iprange_v4_abi1_publication_attempt_report {
    struct iprange_v4_abi1_publication_tuple tuple;
    uint8_t publication_attempt_id[16];
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_local_basename destination_basename;
    struct iprange_v4_abi1_local_identity output_identity;
    struct iprange_v4_abi1_publication_digest output_digest;
    uint32_t publication_policy;
    uint8_t previous_present;
    uint8_t reserved[3];
    struct iprange_v4_abi1_local_identity previous_identity;
    struct iprange_v4_abi1_publication_digest previous_digest;
    struct iprange_v4_abi1_local_identity reservation_identity;
    struct iprange_v4_abi1_creation_security creation_security;
} iprange_v4_abi1_publication_attempt_report;

typedef struct iprange_v4_abi1_optional_u64 {
    uint8_t present;
    uint8_t reserved[7];
    uint64_t value;
} iprange_v4_abi1_optional_u64;

typedef struct iprange_v4_abi1_publication_report {
    uint32_t abi_version;
    uint32_t struct_size;
    struct iprange_v4_abi1_publication_attempt_report attempt;
    uint8_t main_namespace_may_have_been_attempted;
    uint8_t live_lineage_present;
    uint8_t reserved0[2];
    uint32_t publication;
    uint32_t destination_content;
    uint32_t later_canonical;
    uint32_t live_lineage;
    struct iprange_v4_abi1_optional_bytes16 later_attempt_or_sidecar_id;
    struct iprange_v4_abi1_optional_u64 later_selected_transaction_id;
    struct iprange_v4_abi1_optional_bytes16 later_selected_commit_nonce;
    uint32_t main_access_policy;
    uint32_t coordination_access_policy;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
    uint32_t housekeeping;
    uint32_t reserved1;
} iprange_v4_abi1_publication_report;

typedef struct iprange_v4_abi1_validation_generation {
    uint8_t present;
    uint8_t reserved0[3];
    uint32_t address_family;
    uint32_t value_kind;
    uint32_t structure_kind;
    uint8_t value_tag[16];
    uint8_t database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
    uint64_t page_count;
} iprange_v4_abi1_validation_generation;

typedef struct iprange_v4_abi1_validation_progress {
    uint64_t checked_unique_pages;
    uint64_t finding_count;
    uint64_t untraversable_subgraphs;
    struct iprange_v4_abi1_cardinality129 bounded_possible_span_addresses;
    uint8_t has_unbounded_unknown;
    uint8_t reserved[7];
    uint64_t reason_counts[47];
    uint64_t object_counts[17];
} iprange_v4_abi1_validation_progress;

typedef struct iprange_v4_abi1_validation_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint8_t valid;
    uint8_t reserved[7];
    struct iprange_v4_abi1_local_identity file_identity;
    struct iprange_v4_abi1_validation_generation generation;
    struct iprange_v4_abi1_validation_progress progress;
} iprange_v4_abi1_validation_report;

typedef struct iprange_v4_abi1_recovery_candidates_report {
    uint32_t abi_version;
    uint32_t struct_size;
    struct iprange_v4_abi1_local_identity source_identity;
    struct iprange_v4_abi1_validation_progress progress;
} iprange_v4_abi1_recovery_candidates_report;

typedef struct iprange_v4_abi1_logical_counts {
    uint64_t examined;
    uint64_t accepted;
    uint64_t rejected;
} iprange_v4_abi1_logical_counts;

typedef struct iprange_v4_abi1_recovery_facts {
    struct iprange_v4_abi1_logical_counts pages;
    uint64_t pages_io_unreadable;
    struct iprange_v4_abi1_logical_counts ranges;
    struct iprange_v4_abi1_logical_counts catalog_entries;
    struct iprange_v4_abi1_logical_counts membership_entries;
    struct iprange_v4_abi1_logical_counts structure_entries;
    struct iprange_v4_abi1_logical_counts metadata_chunks;
    struct iprange_v4_abi1_logical_counts retirement_records;
    struct iprange_v4_abi1_cardinality129 verified_addresses;
    struct iprange_v4_abi1_cardinality129 rejected_addresses;
    struct iprange_v4_abi1_cardinality129 bounded_possible_span_addresses;
    uint8_t has_unbounded_unknown;
    uint8_t reserved[7];
    uint64_t unknown_envelopes;
} iprange_v4_abi1_recovery_facts;

typedef struct iprange_v4_abi1_recovery_report {
    uint32_t abi_version;
    uint32_t struct_size;
    struct iprange_v4_abi1_recovery_facts facts;
    uint8_t scratch_present;
    uint8_t reserved[7];
    uint8_t scratch_attempt_id[16];
    struct iprange_v4_abi1_local_identity scratch_directory_identity;
    struct iprange_v4_abi1_creation_security scratch_creation_security;
    struct iprange_v4_abi1_publication_report publication;
} iprange_v4_abi1_recovery_report;

typedef struct iprange_v4_abi1_residue_report {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t operation;
    uint32_t classification;
    struct iprange_v4_abi1_local_identity directory_identity;
    struct iprange_v4_abi1_optional_identity coordination_identity;
    struct iprange_v4_abi1_optional_identity main_identity;
    uint32_t main_content;
    uint32_t later_coordination;
    uint32_t access_policy;
    uint32_t cleanup_state;
    uint32_t coordination_cleanup;
    uint32_t housekeeping;
    uint8_t source_present;
    uint8_t publication_present;
    uint8_t reserved[6];
    uint64_t entry_count;
    uint8_t main_tuple_present;
    uint8_t reserved1[7];
    struct iprange_v4_abi1_publication_tuple main_tuple;
    struct iprange_v4_abi1_publication_digest main_digest;
    struct iprange_v4_abi1_publication_report publication;
} iprange_v4_abi1_residue_report;

typedef struct iprange_v4_abi1_recovery_candidate {
    uint32_t abi_version;
    uint32_t struct_size;
    uint32_t label;
    uint32_t reserved;
    struct iprange_v4_abi1_local_identity source_identity;
    uint8_t database_id[16];
    uint64_t transaction_id;
    uint8_t commit_nonce[16];
} iprange_v4_abi1_recovery_candidate;

typedef uint32_t (*iprange_v4_abi1_direct_sink_fn)(void *context,
                                                   const struct iprange_v4_abi1_direct_range *records,
                                                   uint64_t count,
                                                   struct iprange_v4_abi1_callback_failure *failure);

typedef uint32_t (*iprange_v4_abi1_membership_sink_fn)(void *context,
                                                       const struct iprange_v4_abi1_membership_range *records,
                                                       uint64_t count,
                                                       struct iprange_v4_abi1_callback_failure *failure);

typedef uint32_t (*iprange_v4_abi1_network_enrichment_v1_sink_fn)(void *context,
                                                                  const struct iprange_v4_abi1_network_enrichment_v1_range *records,
                                                                  uint64_t count,
                                                                  struct iprange_v4_abi1_callback_failure *failure);

typedef uint32_t (*iprange_v4_abi1_coverage_sink_fn)(void *context,
                                                     const struct iprange_v4_abi1_range *records,
                                                     uint64_t count,
                                                     struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_validation_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint32_t max_open_files;
    uint32_t max_scratch_files;
    uint64_t max_scratch_bytes;
    uint8_t scratch_directory_present;
    uint8_t reserved[7];
    struct iprange_v4_abi1_path scratch_directory;
} iprange_v4_abi1_validation_budget;

typedef struct iprange_v4_abi1_validation_finding {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t sequence;
    uint32_t reason;
    uint32_t object;
    struct iprange_v4_abi1_optional_u32 page_number;
    uint8_t physical_bytes_present;
    uint8_t address_fence_present;
    uint8_t reserved0[6];
    uint64_t physical_start;
    uint64_t physical_end_exclusive;
    struct iprange_v4_abi1_optional_u32 related_page_number;
    struct iprange_v4_abi1_ip address_from;
    struct iprange_v4_abi1_ip address_to;
} iprange_v4_abi1_validation_finding;

typedef uint32_t (*iprange_v4_abi1_validation_finding_sink_fn)(void *context,
                                                               const struct iprange_v4_abi1_validation_finding *records,
                                                               uint64_t count,
                                                               struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_recovery_budget {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t max_heap_bytes;
    uint64_t max_output_pages;
    uint32_t max_open_files;
    uint32_t max_scratch_files;
    uint64_t max_scratch_bytes;
    uint8_t scratch_directory_present;
    uint8_t reserved[7];
    struct iprange_v4_abi1_path scratch_directory;
} iprange_v4_abi1_recovery_budget;

typedef struct iprange_v4_abi1_recovery_unknown {
    uint32_t abi_version;
    uint32_t struct_size;
    uint64_t sequence;
    uint32_t reason;
    uint32_t object;
    struct iprange_v4_abi1_optional_u32 page_number;
    uint8_t physical_bytes_present;
    uint8_t address_fence_present;
    uint8_t contributes_to_possible_span;
    uint8_t has_unbounded_extent;
    uint8_t reserved[4];
    uint64_t physical_start;
    uint64_t physical_end_exclusive;
    struct iprange_v4_abi1_ip address_from;
    struct iprange_v4_abi1_ip address_to;
} iprange_v4_abi1_recovery_unknown;

typedef uint32_t (*iprange_v4_abi1_recovery_unknown_sink_fn)(void *context,
                                                             const struct iprange_v4_abi1_recovery_unknown *records,
                                                             uint64_t count,
                                                             struct iprange_v4_abi1_callback_failure *failure);

typedef uint32_t (*iprange_v4_abi1_direct_source_fn)(void *context,
                                                     struct iprange_v4_abi1_direct_range *records,
                                                     uint64_t capacity,
                                                     uint64_t *count,
                                                     struct iprange_v4_abi1_callback_failure *failure);

typedef struct iprange_v4_abi1_first_seen_removal {
    struct iprange_v4_abi1_range range;
    uint32_t first_seen;
    uint32_t reserved;
    struct iprange_v4_abi1_cardinality129 addresses;
} iprange_v4_abi1_first_seen_removal;

typedef uint32_t (*iprange_v4_abi1_first_seen_removal_sink_fn)(void *context,
                                                               const struct iprange_v4_abi1_first_seen_removal *records,
                                                               uint64_t count,
                                                               struct iprange_v4_abi1_callback_failure *failure);

#ifdef __cplusplus
extern "C" {
#endif // __cplusplus

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_create(const iprange_v4_abi1_membership_scope *const *scopes,
                                                   uint64_t scope_count,
                                                   const struct iprange_v4_abi1_membership_algebra_budget *budget,
                                                   struct iprange_v4_abi1_cancellation cancellation,
                                                   iprange_v4_abi1_membership_algebra **output,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_feeds(const iprange_v4_abi1_membership_algebra *algebra,
                                                  iprange_v4_abi1_feed_name_sink_fn callback,
                                                  void *context,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_count(const iprange_v4_abi1_membership_algebra *algebra,
                                                  struct iprange_v4_abi1_feed_selection_input selection,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  struct iprange_v4_abi1_algebra_count_report *output,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_compare(const iprange_v4_abi1_membership_algebra *algebra,
                                                    struct iprange_v4_abi1_feed_selection_input left,
                                                    struct iprange_v4_abi1_feed_selection_input right,
                                                    struct iprange_v4_abi1_cancellation cancellation,
                                                    struct iprange_v4_abi1_algebra_comparison_report *output,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_publish_set(const iprange_v4_abi1_membership_algebra *algebra,
                                                        struct iprange_v4_abi1_path destination,
                                                        struct iprange_v4_abi1_byte_slice value_tag,
                                                        struct iprange_v4_abi1_algebra_set_operation_input operation,
                                                        struct iprange_v4_abi1_algebra_output_mode_input mode,
                                                        struct iprange_v4_abi1_optional_byte_slice metadata_json,
                                                        uint32_t destination_policy,
                                                        const struct iprange_v4_abi1_algebra_output_budget *budget,
                                                        struct iprange_v4_abi1_cancellation cancellation,
                                                        struct iprange_v4_abi1_algebra_set_report *semantic_output,
                                                        iprange_v4_abi1_report **report_output,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_close(iprange_v4_abi1_membership_algebra *algebra,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_algebra_destroy(iprange_v4_abi1_membership_algebra *algebra,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_open_direct_cursor(const iprange_v4_abi1_reader *reader,
                                                   uint32_t direction,
                                                   const struct iprange_v4_abi1_range *bounds,
                                                   iprange_v4_abi1_cursor **output,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_open_membership_cursor(const iprange_v4_abi1_reader *reader,
                                                       uint32_t direction,
                                                       const struct iprange_v4_abi1_range *bounds,
                                                       iprange_v4_abi1_cursor **output,
                                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_open_network_enrichment_v1_cursor(const iprange_v4_abi1_reader *reader,
                                                                  uint32_t direction,
                                                                  const struct iprange_v4_abi1_range *bounds,
                                                                  iprange_v4_abi1_cursor **output,
                                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_open_feed_cursor(const iprange_v4_abi1_reader *reader,
                                                 const uint8_t *name_pointer,
                                                 uint64_t name_length,
                                                 uint32_t direction,
                                                 const struct iprange_v4_abi1_range *bounds,
                                                 iprange_v4_abi1_cursor **output,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_next_direct(const iprange_v4_abi1_cursor *cursor,
                                            uint8_t *present,
                                            struct iprange_v4_abi1_direct_range *output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_next_membership(const iprange_v4_abi1_cursor *cursor,
                                                uint8_t *present,
                                                struct iprange_v4_abi1_membership_range *output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_next_network_enrichment_v1(const iprange_v4_abi1_cursor *cursor,
                                                           uint8_t *present,
                                                           struct iprange_v4_abi1_network_enrichment_v1_range *output,
                                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_next_coverage(const iprange_v4_abi1_cursor *cursor,
                                              uint8_t *present,
                                              struct iprange_v4_abi1_range *output,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_close(const iprange_v4_abi1_cursor *cursor,
                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cursor_destroy(iprange_v4_abi1_cursor *cursor,
                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_code(const iprange_v4_abi1_error *error,
                                    uint32_t *code,
                                    uint8_t *caller_code_present,
                                    uint64_t *caller_code);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_os_code(const iprange_v4_abi1_error *error,
                                       uint8_t *present,
                                       int64_t *code);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_message_query(const iprange_v4_abi1_error *error,
                                             uint64_t *required);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_message_read(const iprange_v4_abi1_error *error,
                                            struct iprange_v4_abi1_mutable_byte_slice output,
                                            uint64_t *required);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_cause(const iprange_v4_abi1_error *error,
                                     const iprange_v4_abi1_error **cause);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_cleanup_artifact_count(const iprange_v4_abi1_error *error,
                                                      uint64_t *count);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_cleanup_artifact_get(const iprange_v4_abi1_error *error,
                                                    uint64_t index,
                                                    struct iprange_v4_abi1_cleanup_artifact *output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_take_cleanup_guard(iprange_v4_abi1_error *error,
                                                  iprange_v4_abi1_cleanup_guard **guard_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_error_destroy(iprange_v4_abi1_error *error);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_version(void);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_project_history(const iprange_v4_abi1_writer *writer,
                                                const iprange_v4_abi1_reader *last_seen_reader,
                                                const struct iprange_v4_abi1_history_window_input *windows,
                                                uint64_t window_count,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_report **report_output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_create_immutable_feed(struct iprange_v4_abi1_path destination,
                                               uint32_t address_family,
                                               struct iprange_v4_abi1_byte_slice value_tag,
                                               struct iprange_v4_abi1_byte_slice feed_name,
                                               struct iprange_v4_abi1_optional_byte_slice metadata_json,
                                               uint32_t destination_policy,
                                               iprange_v4_abi1_coverage_source_fn source_callback,
                                               void *source_context,
                                               const struct iprange_v4_abi1_immutable_feed_budget *budget,
                                               struct iprange_v4_abi1_cancellation cancellation,
                                               struct iprange_v4_abi1_immutable_feed_report *semantic_output,
                                               iprange_v4_abi1_report **report_output,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_open_immutable_reader(struct iprange_v4_abi1_path source,
                                               iprange_v4_abi1_reader **reader_output,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_open_live_reader(struct iprange_v4_abi1_path source,
                                          struct iprange_v4_abi1_cancellation cancellation,
                                          iprange_v4_abi1_reader **reader_output,
                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_open_live_writer(struct iprange_v4_abi1_path source,
                                          const struct iprange_v4_abi1_transaction_budget *budget,
                                          struct iprange_v4_abi1_cancellation cancellation,
                                          iprange_v4_abi1_writer **writer_output,
                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_close(iprange_v4_abi1_reader *reader,
                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_destroy(iprange_v4_abi1_reader *reader,
                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_destroy(iprange_v4_abi1_writer *writer,
                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_create_live(struct iprange_v4_abi1_path destination,
                                     uint32_t address_family,
                                     uint32_t value_kind,
                                     uint32_t structure_kind,
                                     struct iprange_v4_abi1_byte_slice value_tag,
                                     uint32_t reader_capacity,
                                     struct iprange_v4_abi1_cancellation cancellation,
                                     iprange_v4_abi1_report **report_output,
                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_initialize_live(struct iprange_v4_abi1_path source,
                                         uint32_t reader_capacity,
                                         struct iprange_v4_abi1_cancellation cancellation,
                                         iprange_v4_abi1_report **report_output,
                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reset_live_coordination(struct iprange_v4_abi1_path source,
                                                 uint32_t reader_capacity,
                                                 uint32_t policy,
                                                 struct iprange_v4_abi1_cancellation cancellation,
                                                 iprange_v4_abi1_report **report_output,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_resolve_create_live(struct iprange_v4_abi1_path source,
                                             const iprange_v4_abi1_report *supplied,
                                             uint32_t action,
                                             struct iprange_v4_abi1_cancellation cancellation,
                                             iprange_v4_abi1_report **report_output,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_resolve_live_transition(struct iprange_v4_abi1_path source,
                                                 const iprange_v4_abi1_report *supplied,
                                                 uint32_t action,
                                                 struct iprange_v4_abi1_cancellation cancellation,
                                                 iprange_v4_abi1_report **report_output,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_resolve_interrupted_live_transition(struct iprange_v4_abi1_path source,
                                                             uint32_t action,
                                                             struct iprange_v4_abi1_cancellation cancellation,
                                                             iprange_v4_abi1_report **report_output,
                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_resolve_commit(struct iprange_v4_abi1_path source,
                                        const iprange_v4_abi1_report *supplied,
                                        uint32_t source_mode,
                                        struct iprange_v4_abi1_cancellation cancellation,
                                        iprange_v4_abi1_report **report_output,
                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_list_abandoned_scratch(struct iprange_v4_abi1_path directory,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_artifact_sink_fn sink_callback,
                                                void *sink_context,
                                                iprange_v4_abi1_report **report_output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_list_abandoned_publication_temps(struct iprange_v4_abi1_path directory,
                                                          struct iprange_v4_abi1_cancellation cancellation,
                                                          iprange_v4_abi1_artifact_sink_fn sink_callback,
                                                          void *sink_context,
                                                          iprange_v4_abi1_report **report_output,
                                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_list_abandoned_reservation_artifacts(struct iprange_v4_abi1_path directory,
                                                              struct iprange_v4_abi1_cancellation cancellation,
                                                              iprange_v4_abi1_artifact_sink_fn sink_callback,
                                                              void *sink_context,
                                                              iprange_v4_abi1_report **report_output,
                                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_list_housekeeping_artifacts(struct iprange_v4_abi1_path directory,
                                                     struct iprange_v4_abi1_cancellation cancellation,
                                                     iprange_v4_abi1_housekeeping_sink_fn sink_callback,
                                                     void *sink_context,
                                                     iprange_v4_abi1_report **report_output,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_remove_abandoned_scratch(struct iprange_v4_abi1_path directory,
                                                  struct iprange_v4_abi1_local_identity expected_directory_identity,
                                                  const uint8_t *attempt_id,
                                                  uint32_t ordinal,
                                                  struct iprange_v4_abi1_local_identity expected_artifact_identity,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  iprange_v4_abi1_report **report_output,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_remove_abandoned_publication_temp(struct iprange_v4_abi1_path directory,
                                                           struct iprange_v4_abi1_local_identity expected_directory_identity,
                                                           const uint8_t *publication_attempt_id,
                                                           struct iprange_v4_abi1_local_identity expected_artifact_identity,
                                                           const struct iprange_v4_abi1_publication_tuple *expected_tuple,
                                                           const struct iprange_v4_abi1_publication_digest *expected_digest,
                                                           struct iprange_v4_abi1_cancellation cancellation,
                                                           iprange_v4_abi1_report **report_output,
                                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_remove_abandoned_reservation_artifact(struct iprange_v4_abi1_path directory,
                                                               struct iprange_v4_abi1_local_identity expected_directory_identity,
                                                               const uint8_t *publication_attempt_id,
                                                               struct iprange_v4_abi1_local_identity expected_artifact_identity,
                                                               struct iprange_v4_abi1_cancellation cancellation,
                                                               iprange_v4_abi1_report **report_output,
                                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_remove_housekeeping_artifact(struct iprange_v4_abi1_path directory,
                                                      struct iprange_v4_abi1_local_identity expected_directory_identity,
                                                      const uint8_t *attempt_id,
                                                      uint32_t ordinal,
                                                      struct iprange_v4_abi1_local_identity expected_envelope_identity,
                                                      const struct iprange_v4_abi1_housekeeping_payload *expected_payload,
                                                      struct iprange_v4_abi1_cancellation cancellation,
                                                      iprange_v4_abi1_report **report_output,
                                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_membership(const iprange_v4_abi1_writer *writer,
                                                 struct iprange_v4_abi1_cancellation cancellation,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_ensure(const iprange_v4_abi1_writer *writer,
                                            const uint8_t *name_pointer,
                                            uint64_t name_length,
                                            iprange_v4_abi1_writer_feed_ref **output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_lookup(const iprange_v4_abi1_writer *writer,
                                            const uint8_t *name_pointer,
                                            uint64_t name_length,
                                            iprange_v4_abi1_writer_feed_ref **output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_enumerate(const iprange_v4_abi1_writer *writer,
                                               iprange_v4_abi1_feed_sink_fn callback_fn,
                                               void *context,
                                               uint64_t *count_output,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_rename(const iprange_v4_abi1_writer *writer,
                                            const iprange_v4_abi1_writer_feed_ref *feed,
                                            const uint8_t *name_pointer,
                                            uint64_t name_length,
                                            iprange_v4_abi1_writer_feed_ref **output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_delete(const iprange_v4_abi1_writer *writer,
                                            const iprange_v4_abi1_writer_feed_ref *feed,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_ref_info(const iprange_v4_abi1_writer_feed_ref *feed,
                                              struct iprange_v4_abi1_feed_info *output,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_feed_ref_destroy(iprange_v4_abi1_writer_feed_ref *feed,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_membership_builder_create(const iprange_v4_abi1_writer *writer,
                                                          iprange_v4_abi1_membership_builder **output,
                                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_builder_add_feed(iprange_v4_abi1_membership_builder *builder,
                                                     const iprange_v4_abi1_writer_feed_ref *feed,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_builder_finish(iprange_v4_abi1_membership_builder *builder,
                                                   iprange_v4_abi1_membership_ref **output,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_builder_destroy(iprange_v4_abi1_membership_builder *builder,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_ref_destroy(iprange_v4_abi1_membership_ref *membership,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_membership_apply_ranges(const iprange_v4_abi1_writer *writer,
                                                        const iprange_v4_abi1_membership_ref *membership,
                                                        uint32_t operation,
                                                        iprange_v4_abi1_coverage_source_fn callback,
                                                        void *context,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cleanup_guard_retry(iprange_v4_abi1_cleanup_guard *guard,
                                             uint8_t *changed,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cleanup_guard_close(iprange_v4_abi1_cleanup_guard *guard,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_cleanup_guard_destroy(iprange_v4_abi1_cleanup_guard *guard,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_residue_close(iprange_v4_abi1_residue *residue,
                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_residue_destroy(iprange_v4_abi1_residue *residue,
                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_snapshot_to(struct iprange_v4_abi1_path source,
                                     uint32_t source_mode,
                                     struct iprange_v4_abi1_path destination,
                                     uint32_t destination_policy,
                                     const struct iprange_v4_abi1_snapshot_budget *budget,
                                     struct iprange_v4_abi1_cancellation cancellation,
                                     iprange_v4_abi1_report **report_output,
                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_resolve_publication(struct iprange_v4_abi1_path destination,
                                             const iprange_v4_abi1_report *supplied,
                                             uint32_t action,
                                             struct iprange_v4_abi1_cancellation cancellation,
                                             iprange_v4_abi1_report **report_output,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_inspect_publication_residue(struct iprange_v4_abi1_path destination,
                                                     struct iprange_v4_abi1_cancellation cancellation,
                                                     iprange_v4_abi1_report **report_output,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_remove_publication_residue(iprange_v4_abi1_residue *residue,
                                                    struct iprange_v4_abi1_cancellation cancellation,
                                                    iprange_v4_abi1_report **report_output,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_all_feeds_scope(const iprange_v4_abi1_reader *reader,
                                                const struct iprange_v4_abi1_membership_query_budget *budget,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_membership_scope **output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_named_feeds_scope(const iprange_v4_abi1_reader *reader,
                                                  const struct iprange_v4_abi1_byte_slice *names,
                                                  uint64_t name_count,
                                                  const struct iprange_v4_abi1_membership_query_budget *budget,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  iprange_v4_abi1_membership_scope **output,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_feeds(const iprange_v4_abi1_membership_scope *scope,
                                                iprange_v4_abi1_feed_sink_fn callback,
                                                void *context,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_matching_feeds(const iprange_v4_abi1_reader *reader,
                                               struct iprange_v4_abi1_ip address,
                                               iprange_v4_abi1_feed_name_sink_fn callback,
                                               void *context,
                                               struct iprange_v4_abi1_cancellation cancellation,
                                               struct iprange_v4_abi1_matching_feeds_report *output,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_aggregate(const iprange_v4_abi1_membership_scope *scope,
                                                    uint32_t mode,
                                                    struct iprange_v4_abi1_byte_slice target,
                                                    const struct iprange_v4_abi1_feed_pair_input *pairs,
                                                    uint64_t pair_count,
                                                    iprange_v4_abi1_feed_cardinality_sink_fn cardinality_callback,
                                                    iprange_v4_abi1_feed_overlap_sink_fn overlap_callback,
                                                    void *context,
                                                    struct iprange_v4_abi1_cancellation cancellation,
                                                    struct iprange_v4_abi1_membership_aggregation_report *output,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_join_direct(const iprange_v4_abi1_membership_scope *scope,
                                                      const iprange_v4_abi1_reader *direct_reader,
                                                      const struct iprange_v4_abi1_direct_join_budget *budget,
                                                      iprange_v4_abi1_direct_join_sink_fn callback,
                                                      void *context,
                                                      struct iprange_v4_abi1_cancellation cancellation,
                                                      struct iprange_v4_abi1_direct_join_report *output,
                                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_join_membership(const iprange_v4_abi1_membership_scope *left,
                                                          const iprange_v4_abi1_membership_scope *right,
                                                          iprange_v4_abi1_membership_cross_sink_fn cross_callback,
                                                          iprange_v4_abi1_uncovered_feed_sink_fn uncovered_callback,
                                                          void *context,
                                                          struct iprange_v4_abi1_cancellation cancellation,
                                                          struct iprange_v4_abi1_membership_join_report *output,
                                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_close(iprange_v4_abi1_membership_scope *scope,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_scope_destroy(iprange_v4_abi1_membership_scope *scope,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_database_info(const iprange_v4_abi1_reader *reader,
                                              struct iprange_v4_abi1_database_info *output,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_lookup_direct(const iprange_v4_abi1_reader *reader,
                                              struct iprange_v4_abi1_ip address,
                                              uint8_t *present,
                                              uint32_t *value,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_lookup_membership(const iprange_v4_abi1_reader *reader,
                                                  struct iprange_v4_abi1_ip address,
                                                  iprange_v4_abi1_membership_view **output,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_lookup_feed(const iprange_v4_abi1_reader *reader,
                                            const uint8_t *name_pointer,
                                            uint64_t name_length,
                                            uint8_t *present,
                                            struct iprange_v4_abi1_feed_info *output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_metadata_query(const iprange_v4_abi1_reader *reader,
                                               uint8_t *present,
                                               uint64_t *required,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_metadata_read(const iprange_v4_abi1_reader *reader,
                                              struct iprange_v4_abi1_mutable_byte_slice output,
                                              uint64_t *required,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_word_count(const iprange_v4_abi1_membership_view *view,
                                                    uint32_t *output,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_word(const iprange_v4_abi1_membership_view *view,
                                              uint32_t index,
                                              uint8_t *present,
                                              uint64_t *output,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_read_words(const iprange_v4_abi1_membership_view *view,
                                                    uint32_t start,
                                                    uint64_t *output_pointer,
                                                    uint64_t output_capacity,
                                                    uint64_t *copied,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_contains_index(const iprange_v4_abi1_membership_view *view,
                                                        uint32_t feed_index,
                                                        uint8_t *contains,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_close(iprange_v4_abi1_membership_view *view,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_membership_view_destroy(iprange_v4_abi1_membership_view *view,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_borrowed_membership_view_word_count(const iprange_v4_abi1_borrowed_membership_view *view,
                                                             uint32_t *output,
                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_borrowed_membership_view_word(const iprange_v4_abi1_borrowed_membership_view *view,
                                                       uint32_t index,
                                                       uint8_t *present,
                                                       uint64_t *output,
                                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_borrowed_membership_view_read_words(const iprange_v4_abi1_borrowed_membership_view *view,
                                                             uint32_t start,
                                                             uint64_t *output_pointer,
                                                             uint64_t output_capacity,
                                                             uint64_t *copied,
                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_borrowed_membership_view_contains_index(const iprange_v4_abi1_borrowed_membership_view *view,
                                                                 uint32_t feed_index,
                                                                 uint8_t *contains,
                                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_kind(const iprange_v4_abi1_report *report,
                                     uint32_t *kind);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_scan(const iprange_v4_abi1_report *report,
                                         struct iprange_v4_abi1_scan_report *output,
                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_finish_input(const iprange_v4_abi1_report *report,
                                                 struct iprange_v4_abi1_finish_input_report *output,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_history_projection(const iprange_v4_abi1_report *report,
                                                       struct iprange_v4_abi1_history_projection_report *output,
                                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_history_window(const iprange_v4_abi1_report *report,
                                                   uint64_t index,
                                                   struct iprange_v4_abi1_history_window_report *output,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_commit(const iprange_v4_abi1_report *report,
                                           struct iprange_v4_abi1_commit_report *output,
                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_abort(const iprange_v4_abi1_report *report,
                                          struct iprange_v4_abi1_abort_report *output,
                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_close(const iprange_v4_abi1_report *report,
                                          struct iprange_v4_abi1_close_report *output,
                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_reclaim(const iprange_v4_abi1_report *report,
                                            struct iprange_v4_abi1_reclaim_report *output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_cleanup_artifact_count(const iprange_v4_abi1_report *report,
                                                       uint64_t *count,
                                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_cleanup_artifact_get(const iprange_v4_abi1_report *report,
                                                     uint64_t index,
                                                     struct iprange_v4_abi1_cleanup_artifact *output,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_cause(const iprange_v4_abi1_report *report,
                                      const iprange_v4_abi1_error **cause,
                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_destroy(iprange_v4_abi1_report *report,
                                        iprange_v4_abi1_error **_error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_commit_resolution(const iprange_v4_abi1_report *report,
                                                      struct iprange_v4_abi1_commit_resolution_report *output,
                                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_create(const iprange_v4_abi1_report *report,
                                           struct iprange_v4_abi1_create_report *output,
                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_live_transition(const iprange_v4_abi1_report *report,
                                                    struct iprange_v4_abi1_live_transition_report *output,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_create_resolution(const iprange_v4_abi1_report *report,
                                                      struct iprange_v4_abi1_create_report *output,
                                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_live_transition_resolution(const iprange_v4_abi1_report *report,
                                                               struct iprange_v4_abi1_live_transition_report *output,
                                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_live_residue(const iprange_v4_abi1_report *report,
                                                 struct iprange_v4_abi1_live_residue_report *output,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_publication(const iprange_v4_abi1_report *report,
                                                struct iprange_v4_abi1_publication_report *output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_validation(const iprange_v4_abi1_report *report,
                                               struct iprange_v4_abi1_validation_report *output,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_recovery_candidates(const iprange_v4_abi1_report *report,
                                                        struct iprange_v4_abi1_recovery_candidates_report *output,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_recovery(const iprange_v4_abi1_report *report,
                                             struct iprange_v4_abi1_recovery_report *output,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_get_residue(const iprange_v4_abi1_report *report,
                                            struct iprange_v4_abi1_residue_report *output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_housekeeping_artifact_count(const iprange_v4_abi1_report *report,
                                                            uint64_t *count,
                                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_housekeeping_artifact_get(const iprange_v4_abi1_report *report,
                                                          uint64_t index,
                                                          struct iprange_v4_abi1_housekeeping_artifact *output,
                                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_recovery_candidate_count(const iprange_v4_abi1_report *report,
                                                         uint64_t *count,
                                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_recovery_candidate_get(const iprange_v4_abi1_report *report,
                                                       uint64_t index,
                                                       struct iprange_v4_abi1_recovery_candidate *output,
                                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_take_cleanup_guard(iprange_v4_abi1_report *report,
                                                   iprange_v4_abi1_cleanup_guard **output,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_report_take_residue(iprange_v4_abi1_report *report,
                                             iprange_v4_abi1_residue **output,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_enumerate_feeds(const iprange_v4_abi1_reader *reader,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_feed_sink_fn callback_fn,
                                                void *context,
                                                iprange_v4_abi1_report **report_output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_scan_direct(const iprange_v4_abi1_reader *reader,
                                            uint32_t direction,
                                            const struct iprange_v4_abi1_range *bounds,
                                            struct iprange_v4_abi1_cancellation cancellation,
                                            iprange_v4_abi1_direct_sink_fn callback_fn,
                                            void *context,
                                            iprange_v4_abi1_report **report_output,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_scan_membership(const iprange_v4_abi1_reader *reader,
                                                uint32_t direction,
                                                const struct iprange_v4_abi1_range *bounds,
                                                struct iprange_v4_abi1_cancellation cancellation,
                                                iprange_v4_abi1_membership_sink_fn callback_fn,
                                                void *context,
                                                iprange_v4_abi1_report **report_output,
                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_scan_network_enrichment_v1(const iprange_v4_abi1_reader *reader,
                                                           uint32_t direction,
                                                           const struct iprange_v4_abi1_range *bounds,
                                                           struct iprange_v4_abi1_cancellation cancellation,
                                                           iprange_v4_abi1_network_enrichment_v1_sink_fn callback_fn,
                                                           void *context,
                                                           iprange_v4_abi1_report **report_output,
                                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_scan_feed(const iprange_v4_abi1_reader *reader,
                                          const uint8_t *name_pointer,
                                          uint64_t name_length,
                                          uint32_t direction,
                                          const struct iprange_v4_abi1_range *bounds,
                                          struct iprange_v4_abi1_cancellation cancellation,
                                          iprange_v4_abi1_coverage_sink_fn callback_fn,
                                          void *context,
                                          iprange_v4_abi1_report **report_output,
                                          iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_lookup_network_enrichment_v1(const iprange_v4_abi1_reader *reader,
                                                             struct iprange_v4_abi1_ip address,
                                                             uint8_t *present,
                                                             struct iprange_v4_abi1_network_enrichment_v1 *value,
                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_reader_lookup_network_enrichment_v1_with_membership(const iprange_v4_abi1_reader *reader,
                                                                             struct iprange_v4_abi1_ip address,
                                                                             uint8_t *present,
                                                                             struct iprange_v4_abi1_network_enrichment_v1 *value,
                                                                             iprange_v4_abi1_membership_view **membership,
                                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_structured(const iprange_v4_abi1_writer *writer,
                                                 struct iprange_v4_abi1_cancellation cancellation,
                                                 iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_network_enrichment_v1_intern(const iprange_v4_abi1_writer *writer,
                                                             struct iprange_v4_abi1_network_enrichment_v1 value,
                                                             const iprange_v4_abi1_membership_ref *membership,
                                                             iprange_v4_abi1_structure_ref **output,
                                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_structure_ref_destroy(iprange_v4_abi1_structure_ref *structure,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_structured_assign_ranges(const iprange_v4_abi1_writer *writer,
                                                         const iprange_v4_abi1_structure_ref *structure,
                                                         iprange_v4_abi1_coverage_source_fn callback,
                                                         void *context,
                                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_structured_clear_ranges(const iprange_v4_abi1_writer *writer,
                                                        iprange_v4_abi1_coverage_source_fn callback,
                                                        void *context,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_validate(struct iprange_v4_abi1_path source,
                                  uint32_t mode,
                                  const iprange_v4_abi1_report *candidate_report,
                                  uint64_t candidate_index,
                                  const struct iprange_v4_abi1_validation_budget *budget,
                                  struct iprange_v4_abi1_cancellation cancellation,
                                  iprange_v4_abi1_validation_finding_sink_fn sink_callback,
                                  void *sink_context,
                                  iprange_v4_abi1_report **report_output,
                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_inspect_recovery_candidates(struct iprange_v4_abi1_path source,
                                                     uint32_t source_mode,
                                                     const struct iprange_v4_abi1_validation_budget *budget,
                                                     struct iprange_v4_abi1_cancellation cancellation,
                                                     iprange_v4_abi1_report **report_output,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_recover_immutable(struct iprange_v4_abi1_path source,
                                           const iprange_v4_abi1_report *candidate_report,
                                           uint64_t candidate_index,
                                           struct iprange_v4_abi1_path destination,
                                           const struct iprange_v4_abi1_recovery_budget *budget,
                                           struct iprange_v4_abi1_cancellation cancellation,
                                           iprange_v4_abi1_recovery_unknown_sink_fn sink_callback,
                                           void *sink_context,
                                           iprange_v4_abi1_report **report_output,
                                           iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_recover_live(struct iprange_v4_abi1_path source,
                                      const iprange_v4_abi1_report *candidate_report,
                                      uint64_t candidate_index,
                                      struct iprange_v4_abi1_path destination,
                                      const struct iprange_v4_abi1_recovery_budget *budget,
                                      struct iprange_v4_abi1_cancellation cancellation,
                                      iprange_v4_abi1_recovery_unknown_sink_fn sink_callback,
                                      void *sink_context,
                                      iprange_v4_abi1_report **report_output,
                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_recover_offline(struct iprange_v4_abi1_path source,
                                         const iprange_v4_abi1_report *candidate_report,
                                         uint64_t candidate_index,
                                         struct iprange_v4_abi1_path destination,
                                         const struct iprange_v4_abi1_recovery_budget *budget,
                                         struct iprange_v4_abi1_cancellation cancellation,
                                         iprange_v4_abi1_recovery_unknown_sink_fn sink_callback,
                                         void *sink_context,
                                         iprange_v4_abi1_report **report_output,
                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_create_feed(const iprange_v4_abi1_writer *writer,
                                                  const uint8_t *name_pointer,
                                                  uint64_t name_length,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_replace_feed(const iprange_v4_abi1_writer *writer,
                                                   const uint8_t *name_pointer,
                                                   uint64_t name_length,
                                                   struct iprange_v4_abi1_cancellation cancellation,
                                                   iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_delete_feed(const iprange_v4_abi1_writer *writer,
                                            const uint8_t *name_pointer,
                                            uint64_t name_length,
                                            struct iprange_v4_abi1_cancellation cancellation,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_rename_feed(const iprange_v4_abi1_writer *writer,
                                            const uint8_t *old_pointer,
                                            uint64_t old_length,
                                            const uint8_t *new_pointer,
                                            uint64_t new_length,
                                            struct iprange_v4_abi1_cancellation cancellation,
                                            iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_direct_replacement(const iprange_v4_abi1_writer *writer,
                                                         struct iprange_v4_abi1_cancellation cancellation,
                                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_first_seen_refresh(const iprange_v4_abi1_writer *writer,
                                                         uint32_t refresh_value,
                                                         struct iprange_v4_abi1_cancellation cancellation,
                                                         iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_last_seen_refresh(const iprange_v4_abi1_writer *writer,
                                                        uint32_t refresh_value,
                                                        uint32_t cutoff,
                                                        struct iprange_v4_abi1_cancellation cancellation,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_membership_import(const iprange_v4_abi1_writer *writer,
                                                        const iprange_v4_abi1_reader *source,
                                                        struct iprange_v4_abi1_cancellation cancellation,
                                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_add_coverage_ranges(const iprange_v4_abi1_writer *writer,
                                                    iprange_v4_abi1_coverage_source_fn callback,
                                                    void *context,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_add_direct_ranges(const iprange_v4_abi1_writer *writer,
                                                  iprange_v4_abi1_direct_source_fn callback,
                                                  void *context,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_finish_input(const iprange_v4_abi1_writer *writer,
                                             iprange_v4_abi1_report **report_output,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_finish_first_seen_with_removals(const iprange_v4_abi1_writer *writer,
                                                                iprange_v4_abi1_first_seen_removal_sink_fn callback,
                                                                void *context,
                                                                iprange_v4_abi1_report **report_output,
                                                                iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_metadata_query(const iprange_v4_abi1_writer *writer,
                                               uint8_t *present,
                                               uint64_t *required,
                                               iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_metadata_read(const iprange_v4_abi1_writer *writer,
                                              struct iprange_v4_abi1_mutable_byte_slice output,
                                              uint64_t *required,
                                              iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_set_metadata_json(const iprange_v4_abi1_writer *writer,
                                                  const uint8_t *input_pointer,
                                                  uint64_t input_length,
                                                  struct iprange_v4_abi1_cancellation cancellation,
                                                  uint8_t *changed,
                                                  iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_clear_metadata_json(const iprange_v4_abi1_writer *writer,
                                                    struct iprange_v4_abi1_cancellation cancellation,
                                                    uint8_t *changed,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_begin_direct(const iprange_v4_abi1_writer *writer,
                                             struct iprange_v4_abi1_cancellation cancellation,
                                             iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_direct_assign_ranges(const iprange_v4_abi1_writer *writer,
                                                     iprange_v4_abi1_direct_source_fn callback,
                                                     void *context,
                                                     iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_direct_clear_ranges(const iprange_v4_abi1_writer *writer,
                                                    iprange_v4_abi1_coverage_source_fn callback,
                                                    void *context,
                                                    iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_commit(const iprange_v4_abi1_writer *writer,
                                       iprange_v4_abi1_report **report_output,
                                       iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_abort(const iprange_v4_abi1_writer *writer,
                                      iprange_v4_abi1_report **report_output,
                                      iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_reclaim(const iprange_v4_abi1_writer *writer,
                                        uint64_t max_transactions,
                                        uint64_t max_pages,
                                        struct iprange_v4_abi1_cancellation cancellation,
                                        iprange_v4_abi1_report **report_output,
                                        iprange_v4_abi1_error **error_output);

IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL
uint32_t iprange_v4_abi1_writer_close(const iprange_v4_abi1_writer *writer,
                                      iprange_v4_abi1_report **report_output,
                                      iprange_v4_abi1_error **error_output);

#ifdef __cplusplus
}  // extern "C"
#endif  // __cplusplus

#endif  /* IPRANGE_V4_ABI1_H */
