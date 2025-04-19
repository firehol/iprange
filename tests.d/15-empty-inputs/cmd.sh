#!/bin/bash
# Create test files and directories
touch empty_file
touch empty_list
mkdir -p empty_dir

# Run all the test cases, piping different marker IPs to each
# If any 172.16.x.x IPs appear in output, it means stdin was incorrectly read

# Test cases that should NOT read from stdin
echo "172.16.99.1" | ../../iprange empty_file 2>/dev/null
echo "172.16.99.2" | ../../iprange @empty_list 2>/dev/null
echo "172.16.99.3" | ../../iprange @empty_dir 2>/dev/null
echo "172.16.99.4" | ../../iprange non_existent_file 2>/dev/null
echo "172.16.99.5" | ../../iprange @non_existent_dir 2>/dev/null
echo "172.16.99.6" | ../../iprange input1
echo "172.16.99.7" | ../../iprange @valid_dir

# Test cases that SHOULD read from stdin
echo "192.168.5.0/24" | ../../iprange - 2>/dev/null
echo "192.168.6.0/24" | ../../iprange 2>/dev/null

# Cleanup
rm -rf empty_file empty_list empty_dir
