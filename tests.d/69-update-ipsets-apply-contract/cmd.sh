#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if ! ../../iprange --has-reduce >/dev/null 2>/dev/null; then
    echo "# ERROR: --has-reduce should succeed for update-ipsets"
    exit 1
fi

cat >"$tmpdir/nets" <<'EOF'
10.0.0.0/25
10.0.0.128/25
EOF

cat >"$tmpdir/ips" <<'EOF'
10.0.0.1
10.0.0.2
EOF

if ! ../../iprange "$tmpdir/nets" --ipset-reduce 0 --ipset-reduce-entries 1 --print-prefix "-A tmpset " >"$tmpdir/nets.out" 2>"$tmpdir/nets.err"; then
    echo "# ERROR: reduced net restore generation failed"
    cat "$tmpdir/nets.err"
    exit 1
fi

if ! ../../iprange -1 "$tmpdir/ips" --print-prefix "-A tmpset " >"$tmpdir/ips.out" 2>"$tmpdir/ips.err"; then
    echo "# ERROR: single-IP restore generation failed"
    cat "$tmpdir/ips.err"
    exit 1
fi

count_output=$(../../iprange -C "$tmpdir/ips" 2>"$tmpdir/count.err")
if [ $? -ne 0 ]; then
    echo "# ERROR: -C failed on the apply input"
    cat "$tmpdir/count.err"
    exit 1
fi

for err in "$tmpdir/nets.err" "$tmpdir/ips.err" "$tmpdir/count.err"; do
    if [ -s "$err" ]; then
        echo "# ERROR: apply-related iprange commands should not emit stderr"
        cat "$err"
        exit 1
    fi
done

cat >"$tmpdir/expected.nets" <<'EOF'
-A tmpset 10.0.0.0/24
EOF

cat >"$tmpdir/expected.ips" <<'EOF'
-A tmpset 10.0.0.1
-A tmpset 10.0.0.2
EOF

if ! diff -u "$tmpdir/expected.nets" "$tmpdir/nets.out"; then
    echo "# ERROR: reduced net restore contract changed"
    exit 1
fi

if ! diff -u "$tmpdir/expected.ips" "$tmpdir/ips.out"; then
    echo "# ERROR: single-IP restore contract changed"
    exit 1
fi

if [ "$count_output" != "1,2" ]; then
    echo "# ERROR: -C contract changed for apply input"
    echo "$count_output"
    exit 1
fi

echo "# OK: update-ipsets apply contracts work"
