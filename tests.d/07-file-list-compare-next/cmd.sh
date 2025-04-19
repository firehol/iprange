#!/bin/bash
# Test the @filename feature with compare-next mode
echo "250.250.250.250" | ../../iprange - as hello  @filelist1 --compare-next @filelist2 --header
