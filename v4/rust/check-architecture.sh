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
capi_physical_pattern='iprange_livedb::(mapping|bootstrap|contract|fixed_tree|slotted_page|page_checksum|draft_store|writer_core|reader_core|feed_catalog|membership_(bitmap|dictionary|view)|range_mutation)'
import_reader_pattern='crate::(mapping|feed_catalog|membership_view|range_cursor)|\b(Mapping|MetaV4|CursorState|ProjectionState)\b'
raw_reader_parts_pattern='\b(import_parts|c_abi_parts)\b'
writer_bypass_pattern='crate::(mapping|bootstrap)|crate::draft_store::(Draft|DraftStore)|crate::workflow::compare|\b(Mapping|MetaV4|Bootstrap|DraftStore|Draft)\b|\.(mapping|base|draft|budget|unproved_tail_end)\b'
import_cache_pattern='(^|[[:space:]])mod cache\b|membership_import/cache\.rs'
membership_representation_pattern='membership_dictionary::Interned|\bInterned\b|add_feed_index_to_membership|membership\.(id|word_count)\b|member\.(id|word_count)\b'
untrusted_core_pattern='crate::(reader_core|writer_core|live_reader|live_writer)\b'
publication_bypass_pattern='Directory::open|\bdirectory\.(scan|entry|open_regular|verify_name|unlink_exact|require_absent|sync)\b|live_lock::lock_file_cancellable'
sidecar_namespace_pattern='crate::publication::(namespace|security)|^pub\(crate\) fn (public_identity|parent_identity|identity|identity_any_link|verify_path_any_link|path_identity|open_rw|create_private|remove_exact|install_noreplace|install_replace_discarding|install_exchange|sync_parent|bind_path)\b'
sidecar_lock_pattern='live_lock::(lock|try_lock|unlock|lock_cancellable).*sidecar\.file|live_sidecar::read_header.*sidecar\.file'

assert_detects 'reader-adapter' "$reader_bridge_pattern" 'use crate::mapping::Mapping;'
assert_detects 'public-C' "$capi_physical_pattern" 'use iprange_livedb::fixed_tree;'
assert_detects 'membership-import reader' "$import_reader_pattern" 'use crate::range_cursor;'
assert_detects 'raw-reader-parts' "$raw_reader_parts_pattern" 'reader.c_abi_parts()'
assert_detects 'writer-adapter' "$writer_bypass_pattern" 'use crate::draft_store::DraftStore;'
assert_detects 'membership-import cache' "$import_cache_pattern" 'mod cache;'
assert_detects 'membership representation' "$membership_representation_pattern" 'membership.id'
assert_detects 'untrusted-inspector' "$untrusted_core_pattern" 'use crate::reader_core;'
assert_detects 'publication-adapter' "$publication_bypass_pattern" 'Directory::open(path)'
assert_detects 'sidecar-namespace' "$sidecar_namespace_pattern" 'pub(crate) fn open_rw()'
assert_detects 'sidecar-lock' "$sidecar_lock_pattern" 'live_lock::lock(&sidecar.file)'

bridge="$source_root/c_abi_support.rs"
import_workflow="$source_root/live_writer/membership_import.rs"
readers=(
    "$source_root/database.rs"
    "$source_root/live_reader.rs"
)
mapfile -t writer_adapters < <(
    find "$source_root/live_writer" -type f -name '*.rs' \
        ! -path "$source_root/live_writer/create.rs" \
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
mapfile -t publication_maintenance < <(
    find "$source_root/publication/maintenance" -type f -name '*.rs' \
        ! -name 'common.rs' ! -name '*_test.rs' ! -name '*_tests.rs' -print
    printf '%s\n' "$source_root/publication/maintenance.rs"
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

((status == 0)) || fail 'Rust v4 ownership boundaries were bypassed'

printf '%b%s%b\n' "$GREEN" \
    'Rust v4 architecture gate passes: healthy, untrusted, and publication adapters preserve their ownership boundaries.' "$NC"
