#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

run_case() {
    local name="$1"
    shift
    local stdout="$tmpdir/$name.out"
    local stderr="$tmpdir/$name.err"

    ../../iprange "$@" >"$stdout" 2>"$stderr"
    rc=$?

    if [ $rc -eq 0 ]; then
        echo "# ERROR: invalid numeric option was accepted for case '$name'"
        cat "$stderr"
        exit 1
    fi

    if ! grep -qi "invalid" "$stderr"; then
        echo "# ERROR: expected an invalid-value error for case '$name'"
        cat "$stderr"
        exit 1
    fi
}

run_case dns_threads_alpha --dns-threads x --help
run_case dns_threads_negative --dns-threads -5 --help
run_case reduce_alpha --ipset-reduce xyz --help
run_case reduce_trailing --ipset-reduce 10x --help
run_case reduce_entries_alpha --reduce-entries xyz --help
run_case reduce_entries_negative --reduce-entries -1 --help
run_case min_prefix_trailing --min-prefix 32abc --help

echo "# OK: invalid numeric CLI options are rejected"
