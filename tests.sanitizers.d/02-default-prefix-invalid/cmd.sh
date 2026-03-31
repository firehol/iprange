#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

printf "1.2.3.4\n" >"$tmpdir/input"

../../iprange --default-prefix 64 <"$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: --default-prefix 64 should fail"
    exit 1
fi

if grep -q "runtime error" "$stderr"; then
    echo "# ERROR: sanitizer reported UB instead of a clean validation error"
    cat "$stderr"
    exit 1
fi

if ! grep -Eqi "default prefix|--default-prefix" "$stderr"; then
    echo "# ERROR: expected a default-prefix validation message"
    cat "$stderr"
    exit 1
fi

echo "# OK: invalid default prefix is rejected cleanly"
