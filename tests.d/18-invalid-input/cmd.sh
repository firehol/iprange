#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat <<'EOF' | ../../iprange - input1 >"$tmpdir/stdout" 2>"$tmpdir/stderr"
12345678901234567890
1.2.3.4.5.6.7.8.9.10.11.12.13.14.15.16.17.18.19.20
1.2.3.123456
1.23456.7.8
EOF
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: invalid input from stdin should fail the command"
    cat "$tmpdir/stderr"
    exit 1
fi

if [ -s "$tmpdir/stdout" ]; then
    echo "# ERROR: invalid input should not produce partial stdout"
    cat "$tmpdir/stdout"
    exit 1
fi

for line in 1 2 3 4; do
    if ! grep -q "Cannot understand line No $line from stdin" "$tmpdir/stderr"; then
        echo "# ERROR: expected parse error for stdin line $line"
        cat "$tmpdir/stderr"
        exit 1
    fi
done

echo "# OK: invalid input aborts the command"
