#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if ../../iprange --has-compare >"$tmpdir/out1" 2>"$tmpdir/err1"; then
    if grep -q "compare and reduce is present" "$tmpdir/err1"; then
        echo "# OK: --has-compare exited 0"
    else
        echo "# ERROR: --has-compare stderr was unexpected"
        cat "$tmpdir/err1"
        exit 1
    fi
else
    echo "# ERROR: --has-compare returned non-zero"
    cat "$tmpdir/err1"
    exit 1
fi

if ../../iprange --has-reduce >"$tmpdir/out2" 2>"$tmpdir/err2"; then
    if grep -q "compare and reduce is present" "$tmpdir/err2"; then
        echo "# OK: --has-reduce exited 0"
    else
        echo "# ERROR: --has-reduce stderr was unexpected"
        cat "$tmpdir/err2"
        exit 1
    fi
else
    echo "# ERROR: --has-reduce returned non-zero"
    cat "$tmpdir/err2"
    exit 1
fi
