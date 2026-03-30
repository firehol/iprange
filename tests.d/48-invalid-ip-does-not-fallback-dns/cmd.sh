#!/bin/bash

tmpdir=$(mktemp -d)
stdout="$tmpdir/stdout"
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/input" <<'EOF'
1.2.3.4x
999.999.999.999abc
1.2.3.4 - foo
EOF

../../iprange - <"$tmpdir/input" >"$stdout" 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: malformed IP-like input should fail the command"
    cat "$stderr"
    exit 1
fi

if [ -s "$stdout" ]; then
    echo "# ERROR: malformed IP-like input should not produce IP output"
    cat "$stdout"
    exit 1
fi

if grep -q "DNS:" "$stderr"; then
    echo "# ERROR: malformed IP-like input should not trigger DNS fallback"
    cat "$stderr"
    exit 1
fi

for line in 1 2 3; do
    if ! grep -q "Cannot understand line No $line from stdin" "$stderr"; then
        echo "# ERROR: expected a parse error for line $line"
        cat "$stderr"
        exit 1
    fi
done

echo "# OK: malformed digit-prefixed input is rejected without DNS fallback"
