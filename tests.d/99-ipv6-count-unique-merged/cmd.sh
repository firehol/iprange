#!/bin/bash
# count-unique merges all IPv6 inputs first -> exercises ipset6_merge
../../iprange -6 input1 input2 --count-unique --header
