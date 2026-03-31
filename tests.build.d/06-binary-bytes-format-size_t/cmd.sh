#!/bin/bash

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
srcfile="$script_dir/../../src/ipset_binary.c"

if ! grep -Fq 'invalid number of bytes, found %zu, expected %zu.' "$srcfile"; then
    echo "ipset_binary.c does not use %zu for the bytes diagnostic"
    exit 1
fi

echo "# OK: binary bytes diagnostic uses %zu for size_t"
