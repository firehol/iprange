#!/bin/bash

# Non-UTF-8 argv: a legal POSIX name may hold bytes that are not valid
# UTF-8, and every engine must classify such an argument as an ordinary
# input instead of failing at start-up.
#
# `std::env::args()` in Rust panics while decoding argv, which is exit
# code 101 with no output, and it panics before `--jsonrpc` is examined,
# so it breaks both surfaces. The C reference reads argv as bytes
# (src/iprange.c main(): `while(i < argc)` over `argv[i]`, and the paths
# reach fopen() unchanged), and the Go port reads os.Args, whose strings
# are byte strings. Both answer a file named `a\377b.iprange` with its
# content and exit 0.
#
# The Rust port must therefore carry argv bytes end to end: the open, the
# CSV name column of `as NAME` and the `--print-prefix`/`--print-suffix`
# wrappers are all output the C produces verbatim. v4/rust/iprange-cli/
# src/legacy/argv.rs is the single place that view is taken.
#
# Only exit codes and stdout are compared between engines; stderr bytes
# are pinned by tests.d/108-nonutf8-load-path.
#
# Every case runs through the engine under test (../../iprange, which
# run-tests.sh points at the binary under validation), and, when they are
# installed, through the C reference at $IPRANGE_REFERENCE and the Go port
# at $IPRANGE_GO.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}
GO=${IPRANGE_GO:-}

# One invalid UTF-8 byte, legal in a file name.
BAD=$'\377'

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1
go_used=0
[ -n "$GO" ] && [ -x "$GO" ] && go_used=1

fail() { echo "# ERROR: $*"; return 1; }

check() {
    # check <label> <expected-rc> <expected-stdout> <args...>: the engine
    # must classify this argv exactly, and must never panic.
    local label=$1 expected_rc=$2 expected_stdout=$3
    shift 3
    : >"$tmpdir/err"
    timeout 20 "$IPRANGE" "$@" <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
    local rc=$?
    if [ "$rc" -eq 101 ]; then
        fail "$label: exit 101 - argv decoding panicked instead of classifying the name"
        cat "$tmpdir/err"
        return 1
    fi
    if [ "$rc" -ne "$expected_rc" ]; then
        fail "$label: exit $rc, expected $expected_rc (124 = the run hung)"
        cat "$tmpdir/err"
        return 1
    fi
    if ! cmp -s <(printf '%b' "$expected_stdout") "$tmpdir/out"; then
        fail "$label: stdout is not the reference bytes"
        cat -v "$tmpdir/out"
        return 1
    fi
    if [ "$reference_used" -eq 1 ] && [ "$compare_reference" != no ]; then
        : >"$tmpdir/ref_err"
        timeout 20 "$REFERENCE" "$@" <"$tmpdir/in" >"$tmpdir/ref_out" 2>"$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ]; then
            fail "$label: engine exit $rc but $REFERENCE exit $rrc"
            return 1
        fi
        if ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            fail "$label: engine stdout differs from $REFERENCE"
            cat -v "$tmpdir/ref_out" "$tmpdir/out"
            return 1
        fi
    fi
    if [ "$go_used" -eq 1 ]; then
        timeout 20 "$GO" "$@" <"$tmpdir/in" >"$tmpdir/go_out" 2>/dev/null
        local grc=$?
        if [ "$grc" -ne "$rc" ]; then
            fail "$label: engine exit $rc but $GO exit $grc"
            return 1
        fi
        if ! cmp -s "$tmpdir/go_out" "$tmpdir/out"; then
            fail "$label: engine stdout differs from $GO"
            cat -v "$tmpdir/go_out" "$tmpdir/out"
            return 1
        fi
    fi
    echo "# OK: $label (exit $rc)"
    return 0
}

printf '' >"$tmpdir/in"

# An input file whose name holds an invalid byte loads like any other.
printf '1.2.3.4\n' >"$tmpdir/a${BAD}b.iprange"
printf '5.6.7.8\n'  >"$tmpdir/plain.iprange"
check "input named a\\377b.iprange" 0 '1.2.3.4\n' "$tmpdir/a${BAD}b.iprange" || exit 1
check "missing input named with \\377" 1 '' "$tmpdir/nope${BAD}x.iprange" || exit 1
check "one bad name beside a good one" 0 '1.2.3.4\n5.6.7.8\n' \
    "$tmpdir/a${BAD}b.iprange" "$tmpdir/plain.iprange" || exit 1

# `as NAME` renames the set in the CSV name column: the C prints the
# label bytes it was given (ipset_csv_write_* over ips->filename).
check "as NAME with \\377 in the CSV name" 0 "lab${BAD}el,1,1\n" \
    "$tmpdir/plain.iprange" as "lab${BAD}el" --count-unique-all || exit 1

# The print wrappers are argv bytes too (src/iprange.c print_addr*).
check "--print-prefix with \\377" 0 "x${BAD}y5.6.7.8\n" \
    --print-prefix "x${BAD}y" "$tmpdir/plain.iprange" || exit 1
check "--print-suffix-ips with \\377" 0 "5.6.7.8s${BAD}\n" \
    --print-suffix-ips "s${BAD}" "$tmpdir/plain.iprange" || exit 1

# An option-shaped token that is not an option is a file name, exactly
# as under the C byte comparison, so the run fails with the load error.
check "unrecognized --optimize\\377 is a path" 1 '' \
    "--optimize${BAD}" "$tmpdir/plain.iprange" || exit 1

# An invalid value is a usage failure whose text echoes the argv bytes.
timeout 20 "$IPRANGE" --min-prefix "a${BAD}b" <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
rc=$?
if [ "$rc" -ne 1 ]; then
    echo "# ERROR: --min-prefix with \\377 exited $rc, expected 1"
    cat "$tmpdir/err"
    exit 1
fi
if ! cmp -s "$tmpdir/err" <(printf "iprange: Invalid value 'a%sb' for --min-prefix. It must be between 1 and 32.\n" "$BAD"); then
    echo "# ERROR: --min-prefix with \\377 must echo the argv bytes in its diagnostic"
    cat -v "$tmpdir/err"
    exit 1
fi
echo "# OK: --min-prefix with \\377 (exit 1, diagnostic echoes the argv bytes)"

# --jsonrpc is exclusive: the check must run, which means argv must be
# readable first. The C reference has no --jsonrpc (it treats it as a
# file), so this case is not compared against it.
compare_reference=no
check "--jsonrpc plus a name with \\377" 1 '' --jsonrpc "a${BAD}b" || exit 1
compare_reference=yes

# @directory must classify every entry it stats, including one named with
# an invalid byte: C builds the path as listname + "/" + entry->d_name
# (src/iprange.c FILE_LIST) and opens it, so all content is merged.
mkdir -p "$tmpdir/dir"
printf '9.9.9.9\n' >"$tmpdir/dir/z${BAD}y.txt"
printf '8.8.8.8\n' >"$tmpdir/dir/a.txt"
check "@directory holding a name with \\377" 0 '8.8.8.8\n9.9.9.9\n' "@$tmpdir/dir" || exit 1

# The invocation name in the -h usage text is argv[0], printed in full by
# C usage(argv[0]); a non-UTF-8 argv[0] is a legal exec of a renamed binary.
if command -v python3 >/dev/null 2>&1; then
    timeout 20 python3 -c '
import os, sys
os.execv(sys.argv[1].encode(), [b"prog\xffname", b"-h"])
' "$IPRANGE" <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
    rc=$?
    if [ "$rc" -eq 101 ]; then
        echo "# ERROR: argv[0] with \\377: exit 101 - argv decoding panicked"
        cat "$tmpdir/err"
        exit 1
    fi
    if [ "$rc" -ne 0 ]; then
        echo "# ERROR: argv[0] with \\377 exited $rc, expected 0"
        cat "$tmpdir/err"
        exit 1
    fi
    if ! printf 'Usage: prog%sname [options] file1 file2 file3 ...\n' "$BAD" |
            cmp -s - <(grep -a '^Usage: ' "$tmpdir/out" | head -n 1); then
        echo "# ERROR: argv[0] with \\377 must be echoed verbatim in the usage line"
        cat -v "$tmpdir/out" | head -5
        exit 1
    fi
    echo "# OK: argv[0] with \\377 (exit 0, usage echoes the name bytes)"
else
    echo "# OK: argv[0] with \\377 skipped (python3 not installed)"
fi

# The summary line names no binaries: which reference engines were
# available varies per machine, and the comparisons above only ever add
# failures, never different output.
echo "# OK: 11 non-UTF-8 argv cases matched the pinned exit codes and stdout"
exit 0
