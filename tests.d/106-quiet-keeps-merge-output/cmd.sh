#!/bin/bash

# `--quiet` is diff-only.
#
# The released tool reads the quiet flag at one place per family:
# `if(!quiet) ipset_print(ips, print)` in the diff branch
# (src/iprange.c:1026, src/iprange6_main.c:414), and its own help text
# says so (`--quiet ... for diff mode`). Merge, common, except, compare
# and the count modes keep printing with `--quiet` set, and the diff
# result is still reported through the exit code (1 with a difference, 0
# without).
#
# The Rust print path used to return early whenever quiet was set, which
# silenced every mode. Each case below is compared byte for byte against
# the C reference when one is installed.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}
BOUND=10

printf '1.2.3.4\n5.6.7.8\n'  >"$tmpdir/a.txt"
printf '1.2.3.4\n9.9.9.9\n'  >"$tmpdir/b.txt"
printf '1.2.3.4\n5.6.7.8\n'  >"$tmpdir/a-again.txt"
printf '::1\n::2\n'          >"$tmpdir/a6.txt"
printf '::1\n::3\n'          >"$tmpdir/b6.txt"

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1

fail() { echo "# ERROR: $*"; return 1; }

# check <label> <expected-rc> <expected-stdout> <args...>
check() {
    local label=$1 expected_rc=$2 expected_stdout=$3
    shift 3
    timeout "$BOUND" "$IPRANGE" "$@" </dev/null >"$tmpdir/out" 2>"$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$expected_rc" ]; then
        fail "$label: exit $rc, expected $expected_rc ($(cat -v "$tmpdir/err"))"
        return 1
    fi
    if ! cmp -s <(printf '%b' "$expected_stdout") "$tmpdir/out"; then
        fail "$label: stdout is not the C bytes"
        echo "#   expected: $(printf '%b' "$expected_stdout" | cat -v)"
        echo "#   actual:   $(cat -v "$tmpdir/out")"
        return 1
    fi
    if [ -s "$tmpdir/err" ]; then
        fail "$label: stderr must be empty, got $(cat -v "$tmpdir/err")"
        return 1
    fi
    if [ "$reference_used" -eq 1 ]; then
        timeout "$BOUND" "$REFERENCE" "$@" </dev/null >"$tmpdir/ref_out" 2>"$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" || ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            fail "$label: engine and $REFERENCE disagree (engine $rc / reference $rrc)"
            return 1
        fi
    fi
    echo "# OK: $label (exit $rc)"
    return 0
}

# Every non-diff mode keeps its output with --quiet.
check "merge without --quiet" 0 '1.2.3.4\n5.6.7.8\n9.9.9.9\n' \
    "$tmpdir/a.txt" "$tmpdir/b.txt" || exit 1
check "merge with --quiet keeps its output" 0 '1.2.3.4\n5.6.7.8\n9.9.9.9\n' \
    --quiet "$tmpdir/a.txt" "$tmpdir/b.txt" || exit 1
check "common with --quiet keeps its output" 0 '1.2.3.4\n' \
    --quiet "$tmpdir/a.txt" --common "$tmpdir/b.txt" || exit 1
check "except with --quiet keeps its output" 0 '5.6.7.8\n' \
    --quiet "$tmpdir/a.txt" --except "$tmpdir/b.txt" || exit 1
check "count-unique-all with --quiet keeps its rows" 0 \
    "$tmpdir/a.txt,2,2\n$tmpdir/b.txt,2,2\n" \
    --quiet "$tmpdir/a.txt" "$tmpdir/b.txt" --count-unique-all || exit 1
check "compare with --quiet keeps its row" 0 \
    "$tmpdir/a.txt,$tmpdir/b.txt,2,2,2,2,3,1\n" \
    --quiet "$tmpdir/a.txt" "$tmpdir/b.txt" --compare || exit 1

# Diff output is the one thing --quiet suppresses; the exit code stays.
check "diff without --quiet prints the difference" 1 '5.6.7.8\n9.9.9.9\n' \
    "$tmpdir/a.txt" --diff "$tmpdir/b.txt" || exit 1
check "diff with --quiet is silent and still reports the difference" 1 '' \
    --quiet "$tmpdir/a.txt" --diff "$tmpdir/b.txt" || exit 1
check "equal diff with --quiet is silent and reports 0" 0 '' \
    --quiet "$tmpdir/a.txt" --diff "$tmpdir/a-again.txt" || exit 1

# Same contract in IPv6 mode.
check "-6 merge with --quiet keeps its output" 0 '::1\n::2/127\n' \
    -6 --quiet "$tmpdir/a6.txt" "$tmpdir/b6.txt" || exit 1
check "-6 diff without --quiet prints" 1 '::2\n::3\n' \
    -6 "$tmpdir/a6.txt" --diff "$tmpdir/b6.txt" || exit 1
check "-6 diff with --quiet is silent and keeps rc 1" 1 '' \
    -6 --quiet "$tmpdir/a6.txt" --diff "$tmpdir/b6.txt" || exit 1

if [ "$reference_used" -eq 1 ]; then
    echo "# OK: every case above also matched $REFERENCE byte for byte"
else
    echo "# OK: every case above matched the pinned C bytes ($REFERENCE not installed)"
fi
exit 0
