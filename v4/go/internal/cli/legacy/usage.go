// Exact --help text of the released legacy CLI (copied verbatim from
// src/iprange.c usage(); %s is the program name as invoked and %d is
// the current --dns-threads maximum, exactly like the C format
// call).

package legacy

import "fmt"

const usageText = `iprange manages IP ranges

Usage: %s [options] file1 file2 file3 ...

Options:
multiple options are aliases

Address family:
	--ipv4
	-4
		> Force IPv4 mode.
		Only IPv4 addresses are accepted.
		IPv4-mapped IPv6 (::ffff:x.x.x.x) is
		converted back to IPv4. All other IPv6 input
		is dropped with a single warning.
		This is the default for text input.

	--ipv6
	-6
		> Force IPv6 mode.
		Both IPv6 and IPv4 addresses are accepted.
		IPv4 input is normalized to IPv4-mapped IPv6
		(::ffff:x.x.x.x). Hostnames are resolved for
		both AAAA and A records.


CIDR output modes:
	--optimize
	--combine
	--merge
	--union
	-J
		> MERGE mode (the default)
		Returns all IPs found on all files.
		The resulting set is sorted.

	--common
	--intersect
		> COMMON mode
		Intersect all files to find their common IPs.
		The resulting set is sorted.

	--except
	--exclude-next
		> EXCEPT mode
		Here is how it works:
		(1) merge all files before this parameter (ipset A);
		(2) remove all IPs found in the files after this
		parameter, from ipset A and print what remains.
		The resulting set is sorted.

	--diff
	--diff-next
		> DIFF mode
		Here is how it works:
		(1) merge all files before this parameter (ipset A);
		(2) merge all files after this parameter (ipset B);
		(3) print all differences between A and B, i.e IPs
		found is either A or B, but not both.
		The resulting set is sorted.
		When there are differences between A and B, iprange
		exits with 1, with 0 otherwise.
	--ipset-reduce PERCENT
	--reduce-factor PERCENT
		> IPSET REDUCE mode
		Merge all files and print the merged set,
		but try to reduce the number of prefixes (subnets)
		found, while allowing some increase in entries.
		The PERCENT is how much percent to allow increase
		on the number of entries in order to reduce
		the prefixes (subnets)
		(the internal default PERCENT is 20).
		Use -v to see exactly what it does.
		The resulting set is sorted.

	--ipset-reduce-entries ENTRIES
	--reduce-entries ENTRIES
		> IPSET REDUCE mode
		Allow increasing the entries above PERCENT,
		if they are below ENTRIES
		(the internal default ENTRIES is 16384).


CSV output modes:
	--compare
		> COMPARE ALL mode
		Compare all files with all other files.
		Add --header to get the CSV header too.

	--compare-first
		> COMPARE FIRST mode
		Compare the first file with all other files.
		Add --header to get the CSV header too.

	--compare-next
		> COMPARE NEXT mode
		Compare all the files that appear before this
		parameter, to all files that appear after this
		parameter.
		Add --header to get the CSV header too.

	--count-unique
	-C
		> COUNT UNIQUE mode
		Merge all files and print its counts.
		Add --header to get the CSV header too.

	--count-unique-all
		> COUNT UNIQUE ALL mode
		Print counts for each file.
		Add --header to get the CSV header too.


Controlling input:
	--dont-fix-network
		By default, the network address of all CIDRs
		is used (i.e., 1.1.1.17/24 is read as 1.1.1.0/24):
		this option disables this feature
		(i.e., 1.1.1.17/24 is read as 1.1.1.17-1.1.1.255).

	--default-prefix PREFIX
	-p PREFIX
		Set the default prefix for all IPs without mask
		(the default is 32).


Controlling CIDR output:
	--min-prefix N
		Do not generate prefixes larger than N,
		i.e., if N is 24 then /24 to /32 entries will be
		generated (a /16 network will be generated
		using multiple /24 networks).
		This is useful to optimize netfilter/iptables
		ipsets where each different prefix increases the
		lookup time for each packet whereas the number of
		entries in the ipset do not affect its performance.
		With this setting more entries will be produced
		to accomplish the same match.
		WARNING: misuse of this parameter can create a large
		number of entries in the generated set.

	--prefixes N,N,N, ...
		Enable only the given prefixes to express all CIDRs;
		prefix 32 is always enabled.
		WARNING: misuse of this parameter can create a large
		number of entries in the generated set.

	--print-ranges
	-j
		Print IP ranges (A.A.A.A-B.B.B.B)
		(the default is to print CIDRs (A.A.A.A/B)).
		It only applies when the output is not CSV.

	--print-single-ips
	-1
		Print single IPs;
		this can produce large output
		(the default is to print CIDRs (A.A.A.A/B)).
		It only applies when the output is not CSV.

	--print-binary
		Print binary data:
		this is the fastest way to print a large ipset.
		The result can be read by iprange on the same
		architecture (no conversion of endianness).

	--print-prefix STRING
		Print STRING before each IP, range or CIDR.
		This sets both --print-prefix-ips and
		--print-prefix-nets .

	--print-prefix-ips STRING
		Print STRING before each single IP:
		useful for entering single IPs to a different
		ipset than the networks.

	--print-prefix-nets STRING
		Print STRING before each range or CIDR:
		useful for entering sunbets to a different
		ipset than single IPs.

	--print-suffix STRING
		Print STRING after each IP, range or CIDR.
		This sets both --print-suffix-ips and
		--print-suffix-nets .

	--print-suffix-ips STRING
		Print STRING after each single IP:
		useful for giving single IPs different
		ipset options.

	--print-suffix-nets STRING
		Print STRING after each range or CIDR:
		useful for giving subnets different
		ipset options.

	--quiet
		Do not print the actual ipset.
		Can only be used in DIFF mode.


Controlling CSV output:
	--header
		When the output is CSV, print the header line
		(the default is to not print the header line).


Controlling DNS resolution:
	--dns-threads NUMBER
		The number of parallel DNS queries to execute
		when the input files contain hostnames
		(the default is %d).

	--dns-silent
		Do not print DNS resolution errors
		(the default is to print all DNS related errors).

	--dns-progress
		Print DNS resolution progress bar.


Other options:
	--has-compare
	--has-reduce
	--has-filelist-loading
	--has-directory-loading
	--has-ipv6
		Exits with 0,
		other versions of iprange will exit with 1.
		Use this option in scripts to find if this
		version of iprange is present in a system.

	-v
		Be verbose on stderr.


Getting help:
	--version
		Print version and exit.

	--help
	-h
		Print this message and exit.


Files:
Input files:
	> fileN
		A filename or - for stdin.
		Each filename can be followed by [as NAME]
		to change its name in the CSV output.
		If no filename is given, stdin is assumed.

	> @filename
		A file list containing filenames, one per line.
		Each file in the list is loaded as an individual ipset.
		Comments starting with # or ; are ignored.
		Empty lines are ignored.
		Multiple @filename parameters can be used.
		@filename works with all modes and respects the positional
		nature of the parameters.

	> @directory
		If @filename refers to a directory, all files in that directory
		will be loaded, each as an individual ipset.
		Subdirectories are ignored.
		Multiple @directory parameters can be used.
		@directory works with all modes and respects the positional
		nature of the parameters.

		Files may contain any or all of the following:
		(1) comments starting with hashes (#) or semicolons (;);
		(2) one IP per line (without mask);
		(3) a CIDR per line (A.A.A.A/B);
		(4) an IP range per line (A.A.A.A - B.B.B.B);
		(5) a CIDR range per line (A.A.A.A/B - C.C.C.C/D);
		the range is calculated as the network address of
		A.A.A.A/B to the broadcast address of C.C.C.C/D
		(this is affected by --dont-fix-network);
		(6) CIDRs can be given in either prefix or netmask
		format in all cases (including ranges);
		(7) one hostname per line, to be resolved with DNS
		(if the IP resolves to multiple IPs, all of them
		will be added to the ipset)
		hostnames cannot be given as ranges;
		(8) spaces and empty lines are ignored.

		Any number of files can be given.

`

// Usage renders the help text with the invocation name and the
// current dns-threads maximum (C usage(argv[0])).
func Usage(prog string, dnsThreads uint32) string {
	return fmt.Sprintf(usageText, prog, dnsThreads)
}

// version is the fresh-configure tree version (configure.ac),
// matching a current C oracle build.
const version = "2.1.2_master"

// Version renders the --version output (byte-exact with src/iprange.c
// version()).
func Version() string {
	return fmt.Sprintf(
		"iprange %s\n"+
			"Copyright (C) 2015-2026 Costa Tsaousis for FireHOL (Refactored and extended)\n"+
			"Copyright (C) 2004 Paul Townsend (Adapted)\n"+
			"Copyright (C) 2003 Gabriel L. Somlo (Original)\n"+
			"\n"+
			"License: GPLv2+: GNU GPL version 2 or later <http://gnu.org/licenses/gpl2.html>.\n"+
			"This program comes with ABSOLUTELY NO WARRANTY; This is free software, and\n"+
			"you are welcome to redistribute it under certain conditions;\n"+
			"See COPYING distributed in the source for details.\n",
		version)
}
