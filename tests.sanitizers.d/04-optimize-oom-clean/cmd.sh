#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/oom.c" <<'EOF'
#include "iprange.h"
#include <stdio.h>
#include <stdlib.h>

char *PROG = "unit-optimize-oom";
int debug;
int cidr_use_network = 1;
int default_prefix = 32;

static int malloc_calls;

#undef malloc
void *malloc(size_t);
void *test_malloc(size_t size) {
    malloc_calls++;
    if(malloc_calls == 3)
        return NULL;
    return malloc(size);
}

int main(void) {
    ipset *ips = ipset_create("oom", 0);

    if(!ips) return 2;

    ipset_add_ip_range(ips, 0x0A000001U, 0x0A000001U);
    ipset_add_ip_range(ips, 0x0A000003U, 0x0A000003U);
    ipset_optimize(ips);
    return 0;
}
EOF

if ! "${CC:-clang}" \
    -DHAVE_CONFIG_H \
    -I"$BUILD_DIR" \
    -I../.. \
    -I../../src \
    -fsanitize=address,undefined \
    -g -O1 -fno-omit-frame-pointer \
    -Dmalloc=test_malloc \
    "$tmpdir/oom.c" \
    ../../src/ipset.c \
    ../../src/ipset_optimize.c \
    -o "$tmpdir/oom"; then
    echo "# ERROR: failed to build optimize OOM harness"
    exit 1
fi

"$tmpdir/oom" >/dev/null 2>"$stderr"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: optimize OOM harness should fail"
    exit 1
fi

if grep -Eq "AddressSanitizer|UndefinedBehaviorSanitizer|runtime error|heap-use-after-free" "$stderr"; then
    echo "# ERROR: optimize OOM path triggered sanitizer findings"
    cat "$stderr"
    exit 1
fi

if ! grep -q "Cannot allocate memory" "$stderr"; then
    echo "# ERROR: expected optimize OOM error message"
    cat "$stderr"
    exit 1
fi

echo "# OK: optimize OOM path fails cleanly"
