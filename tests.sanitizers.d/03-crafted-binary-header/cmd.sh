#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/input.txt" <<EOF
10.0.0.1
10.0.0.3
EOF

if ! ../../iprange "$tmpdir/input.txt" --print-binary > "$tmpdir/good.bin" 2>"$tmpdir/save.stderr"; then
    echo "# ERROR: failed to create baseline binary file"
    cat "$tmpdir/save.stderr"
    exit 1
fi

python3 - "$tmpdir/good.bin" "$tmpdir/poc.bin" <<'PY'
import sys

src, dst = sys.argv[1], sys.argv[2]
with open(src, "rb") as f:
    data = f.read()

parts = data.split(b"\n", 7)
header_lines = parts[:7]
payload = parts[7]
records = (1 << 64) - 1023
wrapped_bytes = ((8 * records) + 4) & ((1 << 64) - 1)

out = []
for line in header_lines:
    if line.startswith(b"records "):
        out.append(f"records {records}".encode())
    elif line.startswith(b"bytes "):
        out.append(f"bytes {wrapped_bytes}".encode())
    elif line.startswith(b"lines "):
        out.append(f"lines {records}".encode())
    elif line.startswith(b"unique ips "):
        out.append(f"unique ips {records}".encode())
    else:
        out.append(line)

with open(dst, "wb") as f:
    f.write(b"\n".join(out) + b"\n" + payload)
PY

../../iprange "$tmpdir/poc.bin" >/dev/null 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: crafted binary should be rejected"
    exit 1
fi

if grep -Eq "AddressSanitizer|UndefinedBehaviorSanitizer|runtime error|heap-buffer-overflow" "$stderr"; then
    echo "# ERROR: crafted binary triggered sanitizer findings"
    cat "$stderr"
    exit 1
fi

echo "# OK: crafted binary is rejected cleanly"
