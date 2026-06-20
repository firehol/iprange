#!/bin/bash
# Exclude IPv6 set input2 from input1 -> exercises ipset6_exclude
../../iprange -6 input1 --except input2
