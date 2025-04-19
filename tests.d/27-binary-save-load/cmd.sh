#!/bin/bash

# Binary files: read a plain text file with IPs and write a binary file

echo "172.16.99.1" | ../../iprange input1 --print-binary | ../../iprange
