#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
binary="$tmpdir/input.bin"
trap 'rm -rf "$tmpdir"' EXIT

printf '1.2.3.4\n' | ../../iprange - --print-binary >"$binary"
printf 'JUNK' >>"$binary"

../../iprange "$binary" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: binary input with trailing garbage should fail"
    cat "$stdout"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: malformed binary input should not produce output"
    cat "$stdout"
    exit 1
fi

if ! grep -Eqi "trailing|extra|garbage|invalid|malformed" "$stderr"; then
    echo "# ERROR: expected a binary validation error about trailing data"
    cat "$stderr"
    exit 1
fi

echo "# OK: binary input with trailing garbage is rejected"
