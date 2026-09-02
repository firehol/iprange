#!/usr/bin/env bash

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
GRAY='\033[0;90m'
NC='\033[0m'

run() {
    printf >&2 "${GRAY}%s >${NC} " "$(pwd)"
    printf >&2 '%b' "$YELLOW"
    printf >&2 '%q ' "$@"
    printf >&2 '%b\n' "$NC"

    local exit_code=0
    "$@" || exit_code=$?
    if ((exit_code == 0)); then
        return 0
    fi

    printf >&2 '%b%s%b\n' "$RED" \
        '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━' "$NC"
    printf >&2 "${RED}[ERROR]${NC} Command failed with exit code %d: ${YELLOW}%s${NC}\n" \
        "$exit_code" "$1"
    printf >&2 '%b%s%b' "$RED" '        Full command:' "$NC"
    printf >&2 ' %q' "$@"
    printf >&2 "\n${RED}        Working dir:${NC} %s\n" "$(pwd)"
    printf >&2 '%b%s%b\n' "$RED" \
        '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━' "$NC"
    return "$exit_code"
}

fail() {
    printf >&2 "${RED}[ERROR]${NC} %s\n" "$1"
    exit 1
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
source_root="$script_dir/iprange-livedb/src"
capi_root="$script_dir/iprange-capi/src"

scan() {
    local label=$1
    local pattern=$2
    shift 2

    local matches
    matches=$(rg -n --no-heading --color never -e "$pattern" "$@" || true)
    if [[ -n "$matches" ]]; then
        printf >&2 '%b%s%b\n%s\n' "$RED" "$label" "$NC" "$matches"
        return 1
    fi
}

assert_detects() {
    local label=$1
    local pattern=$2
    local fixture=$3

    if ! printf '%s\n' "$fixture" | rg -q --color never -e "$pattern" -; then
        fail "Architecture detector does not reject its $label fixture"
    fi
}

reader_bridge_pattern='crate::(mapping|bootstrap|feed_catalog|membership_view|range_tree)|\b(Mapping|MetaV4|CursorState|ProjectionState)\b'
reader_adapter_physical_pattern='crate::(mapping|bootstrap|database_file|live_cleanup|live_lock|live_namespace|live_sidecar|path)\b|\b(Mapping|MetaV4|Bootstrap|OpenMode|Sidecar)\b'
capi_physical_pattern='iprange_livedb::(mapping|bootstrap|contract|page_header|bitmap_page|fixed_tree|slotted_page|page_checksum|draft_store|writer_core|reader_core|feed_catalog|membership_(bitmap|dictionary|view)|range_mutation)'
import_reader_pattern='crate::(mapping|feed_catalog|membership_view|range_cursor)|\b(Mapping|MetaV4|CursorState|ProjectionState)\b'
raw_reader_parts_pattern='\b(import_parts|c_abi_parts)\b'
writer_bypass_pattern='crate::(mapping|bootstrap|database_file|draft_store|retirement|range_tree|fixed_tree|slotted_page|page_header|page_io|free_bitmap|membership_dictionary|membership_tree)\b|crate::workflow::compare|\b(Mapping|MetaV4|Bootstrap|PageBudget|DraftStore|Draft)\b|\.(mapping|base|draft|budget|unproved_tail_end)\b'
import_cache_pattern='(^|[[:space:]])mod cache\b|membership_import/cache\.rs'
membership_representation_pattern='membership_dictionary::Interned|\bInterned\b|add_feed_index_to_membership|membership\.(id|word_count)\b|member\.(id|word_count)\b'
snapshot_physical_pattern='crate::(mapping|feed_catalog|metadata)\b|\b(Mapping|MetaV4|CursorState|ProjectionState)\b|membership_view::by_id|source\.(mapping|meta)\('
generation_constructor_pattern='GenerationReader::new'
untrusted_core_pattern='crate::(reader_core|writer_core|live_reader|live_writer)\b'
publication_bypass_pattern='Directory::open|\bdirectory\.(scan|entry|open_regular|verify_name|unlink_exact|require_absent|sync)\b|live_lock::lock_file_cancellable'
sidecar_namespace_pattern='crate::publication::(namespace|security)|^pub\(crate\) fn (public_identity|parent_identity|identity|identity_any_link|verify_path_any_link|path_identity|open_rw|create_private|remove_exact|install_noreplace|install_replace_discarding|install_exchange|sync_parent|bind_path)\b'
sidecar_lock_pattern='live_lock::(lock|try_lock|unlock|lock_cancellable).*sidecar\.file|live_sidecar::read_header.*sidecar\.file'
untrusted_page_layout_pattern='PAGE_MAGIC|page_type::|u(16|32|64)_le\(page,[[:space:]]*(6|8|16|18|20|22|24)\)|page\.byte\((4|5)\)'
untrusted_bitmap_layout_pattern='const (LEAF_WORDS|LEAF_BITS|BRANCH_CHILDREN|LEAF_END|BRANCH_END|MAX_LEVEL)|HEADER_SIZE[[:space:]]*\+.*(index|word)'
untrusted_tree_layout_pattern='CellLayout::Fixed\([0-9]|minimum:[[:space:]]*[0-9]|maximum:[[:space:]]*[0-9]'
untrusted_checksum_pattern='crc32c_source_with_zeroed\(page|u32_le\(page,[[:space:]]*28\)'
empty_database_pattern='fn (empty_meta|write_empty_main)\b|^[[:space:]]*MetaV4[[:space:]]*\{'
copied_tree_leaf_pattern='\b(LeafBuf|MAX_COPIED_LEAF|copy_leaf)\b'
membership_delta_delete_pattern='\btake_first\b|fixed_tree::(delete|delete_existing)\b|LeafU64Mutation::Delete'
sdk_physical_pattern='crate::(mapping|bootstrap|database_file|draft_store|retirement|range_tree|fixed_tree|slotted_page|page_header|page_io|free_bitmap|membership_dictionary|membership_tree|range_mutation)\b|\b(mapping|bootstrap|database_file|draft_store|retirement|range_tree|fixed_tree|slotted_page|page_header|page_io|free_bitmap|membership_dictionary|membership_tree|range_mutation)::|\b(Mapping|MetaV4|Bootstrap|PageBudget|DraftStore|CursorState|ProjectionState)\b'
structured_adapter_physical_pattern='structured_value::(codec|manager|table)\b|\bstructure_(id_root|hash_root|used_root|entry_count|id_limit)\b|page_type::STRUCTURE_(ID|HASH)'
structured_manager_field_pattern='\b(asn|country_id|state_id|city_id|latitude_microdegrees|longitude_microdegrees)\b'

assert_detects 'reader-adapter' "$reader_bridge_pattern" 'use crate::mapping::Mapping;'
assert_detects 'physical reader-adapter' "$reader_adapter_physical_pattern" 'use crate::database_file;'
assert_detects 'public-C' "$capi_physical_pattern" 'use iprange_livedb::fixed_tree;'
assert_detects 'membership-import reader' "$import_reader_pattern" 'use crate::range_cursor;'
assert_detects 'raw-reader-parts' "$raw_reader_parts_pattern" 'reader.c_abi_parts()'
assert_detects 'writer-adapter' "$writer_bypass_pattern" 'use crate::draft_store::DraftStore;'
assert_detects 'membership-import cache' "$import_cache_pattern" 'mod cache;'
assert_detects 'membership representation' "$membership_representation_pattern" 'membership.id'
assert_detects 'snapshot physical access' "$snapshot_physical_pattern" 'source.mapping()'
assert_detects 'generation constructor' "$generation_constructor_pattern" 'GenerationReader::new(mapping)'
assert_detects 'untrusted-inspector' "$untrusted_core_pattern" 'use crate::reader_core;'
assert_detects 'publication-adapter' "$publication_bypass_pattern" 'Directory::open(path)'
assert_detects 'sidecar-namespace' "$sidecar_namespace_pattern" 'pub(crate) fn open_rw()'
assert_detects 'sidecar-lock' "$sidecar_lock_pattern" 'live_lock::lock(&sidecar.file)'
assert_detects 'untrusted page layout' "$untrusted_page_layout_pattern" 'u32_le(page, 20)'
assert_detects 'untrusted bitmap layout' "$untrusted_bitmap_layout_pattern" 'const LEAF_WORDS: usize = 507;'
assert_detects 'untrusted tree layout' "$untrusted_tree_layout_pattern" 'CellLayout::Fixed(44)'
assert_detects 'untrusted checksum layout' "$untrusted_checksum_pattern" 'u32_le(page, 28)'
assert_detects 'empty database construction' "$empty_database_pattern" 'fn empty_meta()'
assert_detects 'copied fixed-tree leaf' "$copied_tree_leaf_pattern" 'struct LeafBuf;'
assert_detects 'per-record membership-delta deletion' \
    "$membership_delta_delete_pattern" 'fixed_tree::delete_existing(store, root, key, retired)'
assert_detects 'public SDK physical access' "$sdk_physical_pattern" \
    'use crate::range_mutation;'
assert_detects 'structured adapter physical access' \
    "$structured_adapter_physical_pattern" 'use crate::structured_value::table;'
assert_detects 'structure-specific field in common manager' \
    "$structured_manager_field_pattern" 'let asn = 64512;'

bridge="$source_root/c_abi_support.rs"
import_workflow="$source_root/live_writer/membership_import.rs"
readers=(
    "$source_root/database.rs"
    "$source_root/live_reader.rs"
)
mapfile -t writer_adapters < <(
    find "$source_root/live_writer" -type f -name '*.rs' \
        ! -name '*_test.rs' ! -name '*_tests.rs' ! -name 'tests.rs' -print
    printf '%s\n' "$source_root/live_writer.rs"
)
mapfile -t untrusted_inspectors < <(
    find \
        "$source_root/validation" \
        "$source_root/recovery" \
        "$source_root/worker" \
        -type f -name '*.rs' \
        ! -name '*_test.rs' ! -name '*_tests.rs' -print
    printf '%s\n' \
        "$source_root/validation.rs" \
        "$source_root/recovery.rs" \
        "$source_root/worker.rs"
)
mapfile -t production_sources < <(
    find "$source_root" -type f -name '*.rs' \
        ! -name '*_test.rs' ! -name '*_tests.rs' -print
)
mapfile -t empty_database_callers < <(
    find "$source_root" -type f -name '*.rs' \
        ! -path "$source_root/database_file.rs" \
        ! -name '*_test.rs' ! -name '*_tests.rs' ! -name 'tests.rs' -print
)
mapfile -t generation_constructor_callers < <(
    find "$source_root" -type f -name '*.rs' \
        ! -path "$source_root/reader_core.rs" \
        ! -path "$source_root/snapshot/source.rs" \
        ! -name '*_test.rs' ! -name '*_tests.rs' -print
)
mapfile -t publication_maintenance < <(
    find "$source_root/publication/maintenance" -type f -name '*.rs' \
        ! -name 'common.rs' ! -name '*_test.rs' ! -name '*_tests.rs' -print
    printf '%s\n' "$source_root/publication/maintenance.rs"
)
sdk_adapters=(
    "$source_root/immutable_feed.rs"
    "$source_root/history.rs"
    "$source_root/live_writer/history_projection.rs"
    "$source_root/membership_query.rs"
)
structured_adapters=(
    "$source_root/database.rs"
    "$source_root/live_reader.rs"
    "$source_root/live_writer/structured.rs"
    "$source_root/immutable_output/structured.rs"
    "$source_root/c_abi_support/reader.rs"
    "$source_root/c_abi_support/writer.rs"
    "$capi_root/structured.rs"
)
structured_manager=(
    "$source_root/structured_value/codec.rs"
    "$source_root/structured_value/manager.rs"
    "$source_root/structured_value/table.rs"
)
while IFS= read -r source; do
    sdk_adapters+=("$source")
done < <(
    find "$source_root/membership_query" -type f -name '*.rs' \
        ! -name '*_test.rs' ! -name '*_tests.rs' ! -name 'tests.rs' -print
)

status=0
run scan 'The C adapter bypasses the logical reader API:' \
    "$reader_bridge_pattern" \
    "$bridge" || status=1
run scan 'The public C layer bypasses the Rust ownership adapter:' \
    "$capi_physical_pattern" \
    "$capi_root" || status=1
run scan 'Membership import bypasses the logical reader API:' \
    "$import_reader_pattern" \
    "$import_workflow" || status=1
run scan 'A public reader exposes raw selected-generation parts:' \
    "$raw_reader_parts_pattern" \
    "${readers[@]}" || status=1
run scan 'A public reader adapter owns physical mapped-file or coordination work:' \
    "$reader_adapter_physical_pattern" \
    "${readers[@]}" || status=1
run scan 'A writer adapter bypasses WriterCore:' \
    "$writer_bypass_pattern" \
    "${writer_adapters[@]}" || status=1
run scan 'Membership import owns its physical cache outside WriterCore:' \
    "$import_cache_pattern" \
    "$import_workflow" || status=1
run scan 'A high-level membership workflow inspects physical membership representation:' \
    "$membership_representation_pattern" \
    "$source_root/live_writer/membership.rs" \
    "$source_root/live_writer/feed_workflow.rs" || status=1
run scan 'Snapshot orchestration or copying bypasses the logical generation reader:' \
    "$snapshot_physical_pattern" \
    "$source_root/snapshot/api.rs" \
    "$source_root/snapshot/build.rs" || status=1
run scan 'A module outside the two mapped-generation owners constructs a logical reader:' \
    "$generation_constructor_pattern" \
    "${generation_constructor_callers[@]}" || status=1
run scan 'Untrusted validation or recovery imports a healthy-file core:' \
    "$untrusted_core_pattern" \
    "${untrusted_inspectors[@]}" || status=1
run scan 'A private-artifact adapter reimplements namespace ownership:' \
    "$publication_bypass_pattern" \
    "${publication_maintenance[@]}" || status=1
run scan 'The mapped sidecar owner also implements live namespace operations:' \
    "$sidecar_namespace_pattern" \
    "$source_root/live_sidecar.rs" || status=1
run scan 'A sidecar caller bypasses the coordination lock/header API:' \
    "$sidecar_lock_pattern" \
    "${production_sources[@]}" || status=1
run scan 'Validation or recovery redefines the common database-page header:' \
    "$untrusted_page_layout_pattern" \
    "${untrusted_inspectors[@]}" || status=1
run scan 'Validation or recovery redefines bitmap-page geometry:' \
    "$untrusted_bitmap_layout_pattern" \
    "${untrusted_inspectors[@]}" || status=1
run scan 'Validation or recovery hard-codes a tree-cell layout:' \
    "$untrusted_tree_layout_pattern" \
    "${untrusted_inspectors[@]}" || status=1
run scan 'Validation or recovery reimplements the database-page checksum field:' \
    "$untrusted_checksum_pattern" \
    "${untrusted_inspectors[@]}" || status=1
run scan 'A module outside the database-file authority constructs an empty database image:' \
    "$empty_database_pattern" \
    "${empty_database_callers[@]}" || status=1
run scan 'A fixed-tree lookup copies a mapped leaf into temporary storage:' \
    "$copied_tree_leaf_pattern" \
    "$source_root/fixed_tree" || status=1
run scan 'Membership deltas are consumed through repeated search/delete operations:' \
    "$membership_delta_delete_pattern" \
    "$source_root/membership_delta.rs" || status=1
run scan 'A public SDK workflow owns physical database operations:' \
    "$sdk_physical_pattern" \
    "${sdk_adapters[@]}" || status=1
run scan 'A structured-value adapter bypasses the common physical manager:' \
    "$structured_adapter_physical_pattern" \
    "${structured_adapters[@]}" || status=1
run scan 'The legacy CLI module imports the v4 database engine:' \
    'iprange_livedb' \
    "$script_dir/iprange-cli/src/legacy" || status=1
run scan 'The common structure manager contains NetworkEnrichmentV1 fields:' \
    "$structured_manager_field_pattern" \
    "${structured_manager[@]}" || status=1

((status == 0)) || fail 'Rust v4 ownership boundaries were bypassed'

printf '%b%s%b\n' "$GREEN" \
    'Rust v4 architecture gate passes: healthy, untrusted, and publication adapters preserve their ownership boundaries.' "$NC"
