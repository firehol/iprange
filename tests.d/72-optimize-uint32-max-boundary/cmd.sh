#!/bin/bash
# Test that ranges touching the UINT32_MAX boundary (255.255.255.255) are not
# incorrectly merged with ranges at 0.0.0.0. The bug: hi + 1 wraps to 0,
# making 0.0.0.0 appear adjacent to 255.255.255.255.
#
# 255.255.255.254/31 = 255.255.255.254-255.255.255.255
# 0.0.0.0/31         = 0.0.0.0-0.0.0.1
# These are at opposite ends of the address space and must NOT be merged.

printf '255.255.255.254/31\n0.0.0.0/31\n' | ../../iprange -
