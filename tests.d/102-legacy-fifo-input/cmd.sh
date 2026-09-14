#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# The released legacy grammar takes input files as plain argv names and
# through @list files, and it opens both with the blocking semantics of
# fopen(): a FIFO has no data until a writer arrives, so the open waits
# for the writer instead of failing. The C reference (src/iprange.h
# FILE_LIST loading through src/main.c) and each ported engine must keep
# that answer for a producer that opens, writes and closes after a delay.
# Adding O_NONBLOCK to these opens turns the wait into an immediate ENXIO
# refusal, which this case reports as a failed run.
ROWS='10.0.0.0-10.0.0.2
10.0.0.5'

expect_merged() {
    # expect_merged <label> <stdout-file>: the merged answer of this input.
    local label=$1 file=$2
    grep -qx '10.0.0.0/31' "$file" || { echo "# ERROR: $label missing the merged range"; cat "$file"; return 1; }
    grep -qx '10.0.0.2' "$file" || { echo "# ERROR: $label missing the single address"; cat "$file"; return 1; }
    grep -qx '10.0.0.5' "$file" || { echo "# ERROR: $label missing the second single address"; cat "$file"; return 1; }
    return 0
}

# Each producer is a direct child of this script, started before its
# reader and waited for after it: capturing a pid through $( ) would let
# the producer inherit the capture pipe and hold it open until its own
# write completed, which cannot happen before the reader exists.
run_named_fifo() {
    # run_named_fifo <label> <fifo> <args...>: read one FIFO that a
    # delayed producer opens, writes and closes.
    local label=$1 fifo=$2
    shift 2
    mkfifo "$fifo"
    (
        sleep 0.5
        printf '%s\n' "$ROWS" >"$fifo"
    ) >"$tmpdir/producer.log" 2>&1 &
    local producer=$!
    local rc
    rc=$(timeout 10 ../../iprange "$@" >"$tmpdir/out" 2>"$tmpdir/err"; echo $?)
    wait "$producer"
    local prc=$?
    if [ "$rc" -ne 0 ]; then
        echo "# ERROR: $label did not complete (cli rc=$rc; 124 = the run hung, any other code = the open refused the FIFO)"
        cat "$tmpdir/err"
        return 1
    fi
    if [ "$prc" -ne 0 ]; then
        echo "# ERROR: $label producer failed (rc=$prc)"
        cat "$tmpdir/producer.log"
        return 1
    fi
    if [ -s "$tmpdir/err" ]; then
        echo "# ERROR: $label must not emit errors"
        cat "$tmpdir/err"
        return 1
    fi
    cp "$tmpdir/out" "$tmpdir/$label.out"
    expect_merged "$label" "$tmpdir/$label.out" || return 1
    return 0
}

# A FIFO named directly on the command line.
run_named_fifo "argv-fifo" "$tmpdir/stream" "$tmpdir/stream" || exit 1

# The same content through the @list form: the list names the FIFO, and
# the open of the listed name waits for the delayed writer.
printf '%s\n' "$tmpdir/stream2" >"$tmpdir/list.txt"
run_named_fifo "list-fifo" "$tmpdir/stream2" "@$tmpdir/list.txt" || exit 1

# A plain file of the same content is the control: the two shapes above
# must answer exactly what the regular file answers.
printf '%s\n' "$ROWS" >"$tmpdir/rows.txt"
timeout 10 ../../iprange "$tmpdir/rows.txt" >"$tmpdir/rows.out" 2>"$tmpdir/rows.err"
rc=$?
if [ "$rc" -ne 0 ]; then
    echo "# ERROR: regular input file failed (rc=$rc)"
    cat "$tmpdir/rows.err"
    exit 1
fi
expect_merged "regular input file" "$tmpdir/rows.out" || exit 1
for shape in argv-fifo list-fifo; do
    diff -q "$tmpdir/rows.out" "$tmpdir/$shape.out" >/dev/null || {
        echo "# ERROR: $shape input differs from the regular-file answer"
        diff -u "$tmpdir/rows.out" "$tmpdir/$shape.out"
        exit 1
    }
done

echo "# OK: legacy argv input waits for a delayed FIFO producer"
