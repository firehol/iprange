#!/bin/bash
# Test the @filename feature with compare mode
echo "250.250.250.250" | ../../iprange --compare - as stdin @filelist input3 --header
