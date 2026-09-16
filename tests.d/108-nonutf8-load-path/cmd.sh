#!/bin/bash

# Load-path bytes: names that are not valid UTF-8 on all three channels.
#
# A POSIX file name is any sequence of non-NUL, non-slash bytes, so a
# legal name may hold 0xFF. The released tool opens those bytes and writes
# them back verbatim in its load diagnostics (`fprintf(stderr, "%s")` over
# the name it was given):
#   - argv:            `iprange: NAME - No such file or directory` +
#                      `iprange: Cannot load ipset: NAME`
#   - @directory:      `iprange: Cannot access NAME: No such file or directory`,
#                      `iprange: No valid files found in directory: NAME`
#   - @file list line: `iprange: ENTRY - No such file or directory` +
#                      `iprange: Cannot load file ENTRY from list NAME (line N)`
#
# Two defects are pinned here. An `@list` record used to be decoded before
# it was opened, which turns `bad\377name` into a path holding U+FFFD: the
# file then does not exist and the run exits 1 where C merges it and exits
# 0. And the diagnostics rendered U+FFFD for such a name, so stderr was
# the wrong byte stream even when the exit code and stdout were right.
#
# tests.d/104-nonutf8-argv pinned only the exit codes and stdout for the
# load-path failures (its header says so); this case pins the stderr bytes,
# which is the load-path error currency. Every expectation is also compared
# against the C reference when one is installed.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}
BOUND=10

# One invalid UTF-8 byte, legal in a file name.
BAD=$'\377'

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1

fail() { echo "# ERROR: $*"; return 1; }

# check_bytes <label> <expected-rc> <expected-stdout-printf> <expected-stderr-printf> <args...>
check_bytes() {
    local label=$1 expected_rc=$2 expected_stdout=$3 expected_stderr=$4
    shift 4
    timeout "$BOUND" "$IPRANGE" "$@" </dev/null >"$tmpdir/out" 2>"$tmpdir/err"
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
        fail "$label: stderr differs from the C bytes"
        echo "#   expected: $(printf '%b' "$expected_stderr" | cat -v)"
        echo "#   actual:   $(cat -v "$tmpdir/err")"
        return 1
    fi
    if [ "$reference_used" -eq 1 ]; then
        timeout "$BOUND" "$REFERENCE" "$@" </dev/null >"$tmpdir/ref_out" 2>"$tmpdir/ref_err"
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

# --- argv -----------------------------------------------------------------
printf '1.2.3.4\n' >"$tmpdir/a${BAD}b.iprange"
check_bytes "input named a\\377b.iprange loads" 0 '1.2.3.4\n' '' \
    "$tmpdir/a${BAD}b.iprange" || exit 1

check_bytes "missing input named with \\377 reports its bytes" 1 '' \
    "iprange: $tmpdir/nope${BAD}x.iprange - No such file or directory\\niprange: Cannot load ipset: $tmpdir/nope${BAD}x.iprange\\n" \
    "$tmpdir/nope${BAD}x.iprange" || exit 1

# A parse failure inside such a file echoes the name and the raw record.
printf '1.2.3.4/99\n' >"$tmpdir/bad${BAD}parse.txt"
check_bytes "parse failure inside a file named with \\377" 1 '' \
    "iprange: Invalid netmask 99\\niprange: Cannot understand line No 1 from $tmpdir/bad${BAD}parse.txt: 1.2.3.4/99\\n\\niprange: Cannot load ipset: $tmpdir/bad${BAD}parse.txt\\n" \
    "$tmpdir/bad${BAD}parse.txt" || exit 1

# --- @directory -----------------------------------------------------------
check_bytes "missing @directory named with \\377" 1 '' \
    "iprange: Cannot access $tmpdir/nodir${BAD}: No such file or directory\\n" \
    "@$tmpdir/nodir${BAD}" || exit 1

mkdir -p "$tmpdir/empty${BAD}"
check_bytes "empty @directory named with \\377" 1 '' \
    "iprange: No valid files found in directory: $tmpdir/empty${BAD}\\n" \
    "@$tmpdir/empty${BAD}" || exit 1

mkdir -p "$tmpdir/dir"
printf '9.9.9.9\n' >"$tmpdir/dir/z${BAD}y.txt"
printf '8.8.8.8\n' >"$tmpdir/dir/a.txt"
check_bytes "@directory holding a name with \\377" 0 '8.8.8.8\n9.9.9.9\n' '' \
    "@$tmpdir/dir" || exit 1

# --- @file list -----------------------------------------------------------
check_bytes "missing @file list named with \\377" 1 '' \
    "iprange: Cannot access $tmpdir/nolist${BAD}.txt: No such file or directory\\n" \
    "@$tmpdir/nolist${BAD}.txt" || exit 1

# The list name and the record it holds can each carry the invalid byte.
printf 'nope%sx.iprange\n' "$BAD" >"$tmpdir/badlist${BAD}.txt"
check_bytes "@file list line naming a missing file with \\377" 1 '' \
    "iprange: nope${BAD}x.iprange - No such file or directory\\niprange: Cannot load file nope${BAD}x.iprange from list $tmpdir/badlist${BAD}.txt (line 1)\\n" \
    "@$tmpdir/badlist${BAD}.txt" || exit 1

# The record must be opened by its bytes: decoding it would open a path
# holding U+FFFD (which does not exist) instead of the file that does.
printf '5.5.5.5\n' >"$tmpdir/listed${BAD}.iprange"
printf '%s/listed%s.iprange\n' "$tmpdir" "$BAD" >"$tmpdir/list-abs.txt"
check_bytes "@file list line naming an existing file with \\377" 0 '5.5.5.5\n' '' \
    "@$tmpdir/list-abs.txt" || exit 1

# A relative record is resolved from the working directory, exactly as the
# C fopen(line) does; run the engine from inside the fixture directory.
printf '4.4.4.4\n' >"$tmpdir/rel${BAD}.iprange"
printf 'rel%s.iprange\n' "$BAD" >"$tmpdir/list-rel.txt"
IPRANGE_ABS=$(cd ../.. && pwd)/iprange
(
    cd "$tmpdir" || exit 1
    timeout "$BOUND" "$IPRANGE_ABS" "@list-rel.txt" </dev/null >"$tmpdir/rel_out" 2>"$tmpdir/rel_err"
    echo $? >"$tmpdir/rel_rc"
)
rc=$(cat "$tmpdir/rel_rc")
if [ "$rc" -ne 0 ] || ! cmp -s <(printf '4.4.4.4\n') "$tmpdir/rel_out"; then
    echo "# ERROR: @list with a relative name containing \\377: exit $rc, stdout $(cat -v "$tmpdir/rel_out"), stderr $(cat -v "$tmpdir/rel_err")"
    exit 1
fi
if [ "$reference_used" -eq 1 ]; then
    (
        cd "$tmpdir" || exit 1
        timeout "$BOUND" "$REFERENCE" "@list-rel.txt" </dev/null >"$tmpdir/ref_rel_out" 2>/dev/null
        echo $? >"$tmpdir/ref_rel_rc"
    )
    if [ "$(cat "$tmpdir/ref_rel_rc")" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_rel_out" "$tmpdir/rel_out"; then
        echo "# ERROR: @list with a relative name containing \\377 disagrees with $REFERENCE"
        exit 1
    fi
fi
echo "# OK: @file list line naming a relative file with \\377 (exit 0)"

if [ "$reference_used" -eq 1 ]; then
    echo "# OK: every load-path diagnostic above matched $REFERENCE byte for byte"
else
    echo "# OK: every load-path diagnostic above matched the pinned C bytes ($REFERENCE not installed)"
fi
exit 0
