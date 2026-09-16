#!/bin/bash

# Unrecognized dash-prefixed arguments: the two C scanners classify them
# differently, and the Rust legacy parser must differ in the same way.
#
# main() (src/iprange.c:515-720) matches its option table and treats every
# remaining token as an input, so `-c` or `--bogus` is a FILE NAME there:
# the run exits 1 with the two load lines. Once the family is IPv6, main()
# skips IPv4 loading (src/iprange.c:722-724) and iprange6_run() re-scans
# argv, where any token that starts with '-', is longer than one byte and
# is not exactly '-' is discarded as a flag (src/iprange6_main.c:176) -
# silently, with no diagnostic, and not counted as an input.
#
# The behaviour class, not one spelling, is what is pinned: unknown long
# and short options, before and after the input files and after an @dir
# source, and the ordering rule that the family active at the token
# position decides the class. Every case is compared byte for byte with
# the C reference when one is installed.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}
BOUND=10

printf '1.2.3.4\n' >"$tmpdir/in4.txt"
printf '::1\n'     >"$tmpdir/in6.txt"
printf '::5\n'     >"$tmpdir/other6.txt"
mkdir -p "$tmpdir/dir6"
printf '::7\n' >"$tmpdir/dir6/f6.txt"

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1

fail() { echo "# ERROR: $*"; return 1; }

# check <label> <expected-rc> <expected-stdout> <expected-stderr> <args...>
check() {
    local label=$1 expected_rc=$2 expected_stdout=$3 expected_stderr=$4
    shift 4
    timeout "$BOUND" "$IPRANGE" "$@" <"$tmpdir/in" >"$tmpdir/out" 2>"$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$expected_rc" ]; then
        fail "$label: exit $rc, expected $expected_rc ($(cat -v "$tmpdir/err"))"
        return 1
    fi
    if ! cmp -s <(printf '%b' "$expected_stdout") "$tmpdir/out"; then
        fail "$label: stdout $(cat -v "$tmpdir/out"), expected $(printf '%b' "$expected_stdout" | cat -v)"
        return 1
    fi
    if ! cmp -s <(printf '%b' "$expected_stderr") "$tmpdir/err"; then
        fail "$label: stderr $(cat -v "$tmpdir/err"), expected $(printf '%b' "$expected_stderr" | cat -v)"
        return 1
    fi
    if [ "$reference_used" -eq 1 ]; then
        timeout "$BOUND" "$REFERENCE" "$@" <"$tmpdir/in" >"$tmpdir/ref_out" 2>"$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" || ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            fail "$label: engine and $REFERENCE disagree (engine $rc / reference $rrc)"
            echo "#   reference stderr: $(cat -v "$tmpdir/ref_err")"
            echo "#   engine stderr:    $(cat -v "$tmpdir/err")"
            return 1
        fi
    fi
    echo "# OK: $label (exit $rc)"
    return 0
}

printf '' >"$tmpdir/in"

# `--optimize` is deliberately absent: it is a real option (the merge
# alias, src/iprange.c:594), so it belongs to no class tested here.
for tok in -c -Z --bogus -- -x; do
    # IPv4: the token is an input path, before or after a real input.
    check "IPv4 unknown $tok before a file" 1 '' \
        "iprange: $tok - No such file or directory\\niprange: Cannot load ipset: $tok\\n" \
        "$tok" "$tmpdir/in4.txt" || exit 1
    check "IPv4 unknown $tok after a file" 1 '' \
        "iprange: $tok - No such file or directory\\niprange: Cannot load ipset: $tok\\n" \
        "$tmpdir/in4.txt" "$tok" || exit 1
    # IPv6: the token is discarded, and the inputs still load.
    check "IPv6 unknown $tok before a file" 0 '::1\n' '' \
        -6 "$tok" "$tmpdir/in6.txt" || exit 1
    check "IPv6 unknown $tok after a file" 0 '::1\n' '' \
        -6 "$tmpdir/in6.txt" "$tok" || exit 1
done

# Between two inputs the skip must not swallow either file.
check "IPv6 unknown token between two files" 0 '::1\n::5\n' '' \
    -6 "$tmpdir/in6.txt" -x "$tmpdir/other6.txt" || exit 1

# Position decides the class: before -6 the family is still IPv4.
check "unknown token before -6 is a path" 1 '' \
    "iprange: --bogus - No such file or directory\\niprange: Cannot load ipset: --bogus\\n" \
    --bogus -6 "$tmpdir/in6.txt" || exit 1
check "unknown token after -6 is skipped" 0 '::1\n' '' \
    -6 --bogus "$tmpdir/in6.txt" || exit 1

# The IPv6 skip does not consume an input and does not disturb the @dir
# source (its entries are IPv6, so the output is not mapped).
check "IPv6 unknown token after @dir" 0 '::7\n' '' \
    -6 "@$tmpdir/dir6" -c || exit 1

# A literal address after the skipped token is a file name, not data.
check "IPv6 literal address after a skipped token is a path" 1 '' \
    'iprange: ::/0 - No such file or directory\niprange: Cannot load ipset: ::/0\n' \
    -6 -c '::/0' || exit 1
# The same invocation with the address on stdin succeeds: that is the
# class the review recorded as "-6 -c ::/0".
printf '::/0\n' >"$tmpdir/in"
check "IPv6 -c with the address on stdin" 0 '::/0\n' '' -6 -c || exit 1
printf '' >"$tmpdir/in"

# A single '-' is never skipped: it is stdin in both families.
printf '9.9.9.9\n' >"$tmpdir/in"
check "IPv4 bare - is stdin" 0 '9.9.9.9\n' '' - || exit 1
printf '::9\n' >"$tmpdir/in"
check "IPv6 bare - is stdin" 0 '::9\n' '' -6 - || exit 1

if [ "$reference_used" -eq 1 ]; then
    echo "# OK: every case above also matched $REFERENCE byte for byte"
else
    echo "# OK: every case above matched the pinned C bytes ($REFERENCE not installed)"
fi
exit 0
