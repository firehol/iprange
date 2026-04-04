#!/bin/bash
# Test basic IPv6 merge (optimize/dedup)

printf "2001:db8::3\n2001:db8::1\n2001:db8::2\nfe80::1\n2001:db8::1\n" | ../../iprange -6
