#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/input" <<'EOF'
!foo
@@@
:
foo!bar
foo/bar
foo bar
EOF

../../iprange - <"$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: malformed hostname-like input should fail the command"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: malformed hostname-like input should not produce IP output"
    cat "$stdout"
    exit 1
fi

if grep -q "DNS:" "$stderr"; then
    echo "# ERROR: malformed hostname-like input should not trigger DNS"
    cat "$stderr"
    exit 1
fi

if grep -q "Ignoring text after hostname" "$stderr"; then
    echo "# ERROR: malformed hostname-like input should not be partially accepted as a hostname"
    cat "$stderr"
    exit 1
fi

for line in 1 2 3 4 5 6; do
    if ! grep -q "Cannot understand line No $line from stdin" "$stderr"; then
        echo "# ERROR: expected a parse error for line $line"
        cat "$stderr"
        exit 1
    fi
done

echo "# OK: malformed hostname-like input is rejected before DNS"
