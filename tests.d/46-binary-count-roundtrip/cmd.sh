#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/input.txt" <<EOF
10.0.0.1
10.0.0.1
10.0.0.1
EOF

text_output=$(../../iprange "$tmpdir/input.txt" -C 2>"$tmpdir/text.stderr")
binary_path="$tmpdir/input.bin"

if [ -s "$tmpdir/text.stderr" ]; then
    echo "# ERROR: unexpected stderr when counting text input"
    cat "$tmpdir/text.stderr"
    exit 1
fi

if ! ../../iprange "$tmpdir/input.txt" --print-binary > "$binary_path" 2>"$tmpdir/binary-save.stderr"; then
    echo "# ERROR: failed to save binary ipset"
    cat "$tmpdir/binary-save.stderr"
    exit 1
fi

if [ -s "$tmpdir/binary-save.stderr" ]; then
    echo "# ERROR: unexpected stderr when saving binary ipset"
    cat "$tmpdir/binary-save.stderr"
    exit 1
fi

binary_output=$(../../iprange "$binary_path" -C 2>"$tmpdir/binary-load.stderr")

if [ -s "$tmpdir/binary-load.stderr" ]; then
    echo "# ERROR: unexpected stderr when counting binary input"
    cat "$tmpdir/binary-load.stderr"
    exit 1
fi

if [ "$text_output" != "$binary_output" ]; then
    echo "# ERROR: binary round-trip changed count output"
    echo "text=$text_output"
    echo "binary=$binary_output"
    exit 1
fi

echo "# OK: binary round-trip preserves count output"
