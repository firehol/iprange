#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

write_binary_case() {
    local path="$1"
    local record_size="$2"
    local records="$3"
    local bytes="$4"
    local lines="$5"
    local unique="$6"

    {
        printf 'iprange binary format v1.0\n'
        printf 'optimized\n'
        printf 'record size %s\n' "$record_size"
        printf 'records %s\n' "$records"
        printf 'bytes %s\n' "$bytes"
        printf 'lines %s\n' "$lines"
        printf 'unique ips %s\n' "$unique"
        printf '\x4D\x3C\x2B\x1A'
    } >"$path"
}

run_case() {
    local name="$1"
    shift
    local file="$tmpdir/$name.bin"
    local stdout="$tmpdir/$name.out"
    local stderr="$tmpdir/$name.err"

    write_binary_case "$file" "$@"

    ../../iprange "$file" >"$stdout" 2>"$stderr"
    rc=$?

    if [ $rc -eq 0 ]; then
        echo "# ERROR: malformed binary metadata was accepted for case '$name'"
        cat "$stderr"
        exit 1
    fi

    if [ -s "$stdout" ]; then
        echo "# ERROR: malformed binary metadata should not produce output for case '$name'"
        cat "$stdout"
        exit 1
    fi
}

run_case record_size_trailing 8garbage 0 4 0 0
run_case records_alpha 8 xyz 4 0 0
run_case records_hexlike 8 0x10 4 0 0
run_case bytes_trailing 8 0 4junk 0 0
run_case lines_trailing 8 0 4 0junk 0
run_case unique_trailing 8 0 4 0 0junk

echo "# OK: malformed binary metadata is rejected"
