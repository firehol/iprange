/*
 * Copyright (C) 2003-2017 Costa Tsaousis <costa@tsaousis.gr>
 * Copyright (C) 2012-2017 Phil Whineray <phil@sanewall.org>
 * Copyright (C) 2003 Gabriel L. Somlo
 */
#include <iprange.h>
#include <iprange6.h>
#include <ipset6.h>
#include <ipset6_print.h>
#include <ipset6_binary.h>
#include <ipset6_load.h>
#include <sys/stat.h>
#include <dirent.h>
#include <limits.h>

char *PROG;
int debug;
int cidr_use_network = 1;
int default_prefix = 32;

/* address family: 0 = default (IPv4), 4 = explicit IPv4, 6 = explicit IPv6 */
int active_family = 0;

/* count of IPv6 lines dropped in IPv4 mode (for one-time warning) */
unsigned long ipv6_dropped_in_ipv4_mode = 0;

/* forward declaration for IPv6 mode execution */
extern int iprange6_run(int argc, char **argv, int mode, IPSET_PRINT_CMD print,
                        int header, int quiet, size_t ipset_reduce_factor,
                        size_t ipset_reduce_min_accepted);

static inline uint64_t ipset_report_unique_ips(ipset *ips, size_t *entries)
{
    uint64_t unique_ips = ipset_unique_ips(ips);

    if(entries) *entries = ips->entries;
    return unique_ips;
}

/* ----------------------------------------------------------------------------
 * usage()
 *
 * print help for the user
 *
 */

static void usage(const char *me) {
    fprintf(stdout,
        "iprange manages IP ranges\n"
        "\n"
        "Usage: %s [options] file1 file2 file3 ...\n"
        "\n"
        "Options:\n"
        "multiple options are aliases\n"
        "\n"
        "Address family:\n"
        "	--ipv4\n"
        "	-4\n"
        "		> Force IPv4 mode.\n"
        "		Only IPv4 addresses are accepted.\n"
        "		IPv4-mapped IPv6 (::ffff:x.x.x.x) is\n"
        "		converted back to IPv4. All other IPv6 input\n"
        "		is dropped with a single warning.\n"
        "		This is the default for text input.\n"
        "\n"
        "	--ipv6\n"
        "	-6\n"
        "		> Force IPv6 mode.\n"
        "		Both IPv6 and IPv4 addresses are accepted.\n"
        "		IPv4 input is normalized to IPv4-mapped IPv6\n"
        "		(::ffff:x.x.x.x). Hostnames are resolved for\n"
        "		both AAAA and A records.\n"
        "\n"
        "\n"
        "CIDR output modes:\n"
        "	--optimize\n"
        "	--combine\n"
        "	--merge\n"
        "	--union\n"
        "	-J\n"
        "		> MERGE mode (the default)\n"
        "		Returns all IPs found on all files.\n"
        "		The resulting set is sorted.\n"
        "\n"
        "	--common\n"
        "	--intersect\n"
        "		> COMMON mode\n"
        "		Intersect all files to find their common IPs.\n"
        "		The resulting set is sorted.\n"
        "\n"
        "	--except\n"
        "	--exclude-next\n"
        "		> EXCEPT mode\n"
        "		Here is how it works:\n"
        "		(1) merge all files before this parameter (ipset A);\n"
        "		(2) remove all IPs found in the files after this\n"
        "		parameter, from ipset A and print what remains.\n"
        "		The resulting set is sorted.\n"
        "\n"
        "	--diff\n"
        "	--diff-next\n"
        "		> DIFF mode\n"
        "		Here is how it works:\n"
        "		(1) merge all files before this parameter (ipset A);\n"
        "		(2) merge all files after this parameter (ipset B);\n"
        "		(3) print all differences between A and B, i.e IPs\n"
        "		found is either A or B, but not both.\n"
        "		The resulting set is sorted.\n"
        "		When there are differences between A and B, iprange\n"
        "		exits with 1, with 0 otherwise."
        "\n"
        "	--ipset-reduce PERCENT\n"
        "	--reduce-factor PERCENT\n"
        "		> IPSET REDUCE mode\n"
        "		Merge all files and print the merged set,\n"
        "		but try to reduce the number of prefixes (subnets)\n"
        "		found, while allowing some increase in entries.\n"
        "		The PERCENT is how much percent to allow increase\n"
        "		on the number of entries in order to reduce\n"
        "		the prefixes (subnets)\n"
        "		(the internal default PERCENT is 20).\n"
        "		Use -v to see exactly what it does.\n"
        "		The resulting set is sorted.\n"
        "\n"
        "	--ipset-reduce-entries ENTRIES\n"
        "	--reduce-entries ENTRIES\n"
        "		> IPSET REDUCE mode\n"
        "		Allow increasing the entries above PERCENT,\n"
        "		if they are below ENTRIES\n"
        "		(the internal default ENTRIES is 16384).\n"
#if 0
        "\n"
        "	--histogram\n"
        "		> IPSET HISTOGRAM mode\n"
        "		Maintain histogram data for ipset and\n"
        "   dump current status.\n"
        "\n"
        "	--histogram-dir PATH\n"
        "		> IPSET HISTOGRAM mode\n"
        "		Specify where to keep histogram data.\n"
#endif
        "\n"
        "\n"
        "CSV output modes:\n"
        "	--compare\n"
        "		> COMPARE ALL mode\n"
        "		Compare all files with all other files.\n"
        "		Add --header to get the CSV header too.\n"
        "\n"
        "	--compare-first\n"
        "		> COMPARE FIRST mode\n"
        "		Compare the first file with all other files.\n"
        "		Add --header to get the CSV header too.\n"
        "\n"
        "	--compare-next\n"
        "		> COMPARE NEXT mode\n"
        "		Compare all the files that appear before this\n"
        "		parameter, to all files that appear after this\n"
        "		parameter.\n"
        "		Add --header to get the CSV header too.\n"
        "\n"
        "	--count-unique\n"
        "	-C\n"
        "		> COUNT UNIQUE mode\n"
        "		Merge all files and print its counts.\n"
        "		Add --header to get the CSV header too.\n"
        "\n"
        "	--count-unique-all\n"
        "		> COUNT UNIQUE ALL mode\n"
        "		Print counts for each file.\n"
        "		Add --header to get the CSV header too.\n"
        "\n"
        "\n"
        "Controlling input:\n"
        "	--dont-fix-network\n"
        "		By default, the network address of all CIDRs\n"
        "		is used (i.e., 1.1.1.17/24 is read as 1.1.1.0/24):\n"
        "		this option disables this feature\n"
        "		(i.e., 1.1.1.17/24 is read as 1.1.1.17-1.1.1.255).\n"
        "\n"
        "	--default-prefix PREFIX\n"
        "	-p PREFIX\n"
        "		Set the default prefix for all IPs without mask\n"
        "		(the default is 32).\n"
        "\n"
        "\n"
        "Controlling CIDR output:\n"
        "	--min-prefix N\n"
        "		Do not generate prefixes larger than N,\n"
        "		i.e., if N is 24 then /24 to /32 entries will be\n"
        "		generated (a /16 network will be generated\n"
        "		using multiple /24 networks).\n"
        "		This is useful to optimize netfilter/iptables\n"
        "		ipsets where each different prefix increases the\n"
        "		lookup time for each packet whereas the number of\n"
        "		entries in the ipset do not affect its performance.\n"
        "		With this setting more entries will be produced\n"
        "		to accomplish the same match.\n"
        "		WARNING: misuse of this parameter can create a large\n"
        "		number of entries in the generated set.\n"
        "\n"
        "	--prefixes N,N,N, ...\n"
        "		Enable only the given prefixes to express all CIDRs;\n"
        "		prefix 32 is always enabled.\n"
        "		WARNING: misuse of this parameter can create a large\n"
        "		number of entries in the generated set.\n"
        "\n"
        "	--print-ranges\n"
        "	-j\n"
        "		Print IP ranges (A.A.A.A-B.B.B.B)\n"
        "		(the default is to print CIDRs (A.A.A.A/B)).\n"
        "		It only applies when the output is not CSV.\n"
        "\n"
        "	--print-single-ips\n"
        "	-1\n"
        "		Print single IPs;\n"
        "		this can produce large output\n"
        "		(the default is to print CIDRs (A.A.A.A/B)).\n"
        "		It only applies when the output is not CSV.\n"
        "\n"
        "	--print-binary\n"
        "		Print binary data:\n"
        "		this is the fastest way to print a large ipset.\n"
        "		The result can be read by iprange on the same\n"
        "		architecture (no conversion of endianness).\n"
        "\n"
        "	--print-prefix STRING\n"
        "		Print STRING before each IP, range or CIDR.\n"
        "		This sets both --print-prefix-ips and\n"
        "		--print-prefix-nets .\n"
        "\n"
        "	--print-prefix-ips STRING\n"
        "		Print STRING before each single IP:\n"
        "		useful for entering single IPs to a different\n"
        "		ipset than the networks.\n"
        "\n"
        "	--print-prefix-nets STRING\n"
        "		Print STRING before each range or CIDR:\n"
        "		useful for entering sunbets to a different\n"
        "		ipset than single IPs.\n"
        "\n"
        "	--print-suffix STRING\n"
        "		Print STRING after each IP, range or CIDR.\n"
        "		This sets both --print-suffix-ips and\n"
        "		--print-suffix-nets .\n"
        "\n"
        "	--print-suffix-ips STRING\n"
        "		Print STRING after each single IP:\n"
        "		useful for giving single IPs different\n"
        "		ipset options.\n"
        "\n"
        "	--print-suffix-nets STRING\n"
        "		Print STRING after each range or CIDR:\n"
        "		useful for giving subnets different\n"
        "		ipset options.\n"
        "\n"
        "	--quiet\n"
        "		Do not print the actual ipset.\n"
        "		Can only be used in DIFF mode.\n"
        "\n"
        "\n"
        "Controlling CSV output:\n"
        "	--header\n"
        "		When the output is CSV, print the header line\n"
        "		(the default is to not print the header line).\n"
        "\n"
        "\n"
        "Controlling DNS resolution:\n"
        "	--dns-threads NUMBER\n"
        "		The number of parallel DNS queries to execute\n"
        "		when the input files contain hostnames\n"
        "		(the default is %d).\n"
        "\n"
        "	--dns-silent\n"
        "		Do not print DNS resolution errors\n"
        "		(the default is to print all DNS related errors).\n"
        "\n"
        "	--dns-progress\n"
        "		Print DNS resolution progress bar.\n"
        "\n"
        "\n"
        "Other options:\n"
        "	--has-compare\n"
        "	--has-reduce\n"
        "	--has-filelist-loading\n"
        "	--has-directory-loading\n"
        "	--has-ipv6\n"
        "		Exits with 0,\n"
        "		other versions of iprange will exit with 1.\n"
        "		Use this option in scripts to find if this\n"
        "		version of iprange is present in a system.\n"
        "\n"
        "	-v\n"
        "		Be verbose on stderr.\n"
        "\n"
        "\n"
        "Getting help:\n"
        "	--version\n"
        "		Print version and exit.\n"
        "\n"
        "	--help\n"
        "	-h\n"
        "		Print this message and exit.\n"
        "\n"
        "\n"
        "Files:\n"
        "Input files:\n"
        "	> fileN\n"
        "		A filename or - for stdin.\n"
        "		Each filename can be followed by [as NAME]\n"
        "		to change its name in the CSV output.\n"
        "		If no filename is given, stdin is assumed.\n"
        "\n"
        "	> @filename\n"
        "		A file list containing filenames, one per line.\n"
        "		Each file in the list is loaded as an individual ipset.\n"
        "		Comments starting with # or ; are ignored.\n"
        "		Empty lines are ignored.\n"
        "		Multiple @filename parameters can be used.\n"
        "		@filename works with all modes and respects the positional\n"
        "		nature of the parameters.\n"
        "\n"
        "	> @directory\n"
        "		If @filename refers to a directory, all files in that directory\n"
        "		will be loaded, each as an individual ipset.\n"
        "		Subdirectories are ignored.\n"
        "		Multiple @directory parameters can be used.\n"
        "		@directory works with all modes and respects the positional\n"
        "		nature of the parameters.\n"
        "\n"
        "		Files may contain any or all of the following:\n"
        "		(1) comments starting with hashes (#) or semicolons (;);\n"
        "		(2) one IP per line (without mask);\n"
        "		(3) a CIDR per line (A.A.A.A/B);\n"
        "		(4) an IP range per line (A.A.A.A - B.B.B.B);\n"
        "		(5) a CIDR range per line (A.A.A.A/B - C.C.C.C/D);\n"
        "		the range is calculated as the network address of\n"
        "		A.A.A.A/B to the broadcast address of C.C.C.C/D\n"
        "		(this is affected by --dont-fix-network);\n"
        "		(6) CIDRs can be given in either prefix or netmask\n"
        "		format in all cases (including ranges);\n"
        "		(7) one hostname per line, to be resolved with DNS\n"
        "		(if the IP resolves to multiple IPs, all of them\n"
        "		will be added to the ipset)\n"
        "		hostnames cannot be given as ranges;\n"
        "		(8) spaces and empty lines are ignored.\n"
        "\n"
        "		Any number of files can be given.\n"
        "\n"
        , me, dns_threads_max);
    exit(0);
}


/* ----------------------------------------------------------------------------
 * version()
 *
 * print version for the user
 *
 */

static void version() {
    fprintf(stdout,
        "iprange " VERSION "\n"
        "Copyright (C) 2015-2017 Costa Tsaousis for FireHOL (Refactored and extended)\n"
        "Copyright (C) 2004 Paul Townsend (Adapted)\n"
        "Copyright (C) 2003 Gabriel L. Somlo (Original)\n"
        "\n"
        "License: GPLv2+: GNU GPL version 2 or later <http://gnu.org/licenses/gpl2.html>.\n"
        "This program comes with ABSOLUTELY NO WARRANTY; This is free software, and\n"
        "you are welcome to redistribute it under certain conditions;\n"
        "See COPYING distributed in the source for details.\n"
        );
    exit(0);
}

static void ipset_chain_append(ipset **head, ipset **tail, ipset *ips)
{
    ips->next = NULL;
    ips->prev = *tail;

    if(*tail) (*tail)->next = ips;
    else *head = ips;

    *tail = ips;
}

static int compare_pathnames(const void *left, const void *right)
{
    const char * const *a = left;
    const char * const *b = right;

    return strcmp(*a, *b);
}

static void invalid_option_value(const char *option, const char *value, const char *expected)
{
    fprintf(stderr, "%s: Invalid value '%s' for %s. %s\n", PROG, value, option, expected);
    exit(1);
}

static long parse_long_option_or_die(const char *option, const char *value, long min, long max, const char *expected)
{
    char *end = NULL;
    long parsed;

    errno = 0;
    parsed = strtol(value, &end, 10);
    if(errno || !end || end == value || *end != '\0' || parsed < min || parsed > max)
        invalid_option_value(option, value, expected);

    return parsed;
}

static size_t parse_size_option_or_die(const char *option, const char *value, size_t min, size_t max, const char *expected)
{
    char *end = NULL;
    unsigned long long parsed;

    if(!value || *value < '0' || *value > '9')
        invalid_option_value(option, value?value:"", expected);

    errno = 0;
    parsed = strtoull(value, &end, 10);
    if(errno || !end || end == value || *end != '\0' || parsed < min || parsed > max)
        invalid_option_value(option, value, expected);

    return (size_t)parsed;
}

static void free_pathnames(char **files, size_t entries)
{
    size_t i;

    for(i = 0; i < entries; i++)
        free(files[i]);

    free(files);
}

/*#define MODE_HISTOGRAM 11 */

int main(int argc, char **argv) {
/*	char histogram_dir[FILENAME_MAX + 1] = "/var/lib/iprange"; */

    struct timeval start_dt, load_dt, print_dt, stop_dt;

    size_t ipset_reduce_factor = 120;
    size_t ipset_reduce_min_accepted = 16384;
    int ret = 0, quiet = 0;
    int inputs = 0;

    ipset *root = NULL, *root_last = NULL, *ips = NULL, *first = NULL, *second = NULL, *second_last = NULL;
    int i, mode = MODE_COMBINE, header = 0, read_second = 0;
    IPSET_PRINT_CMD print = PRINT_CIDR;

    gettimeofday(&start_dt, NULL);

    if ((PROG = strrchr(argv[0], '/')))
        PROG++;
    else
        PROG = argv[0];

    for(i = 1; i < argc ; i++) {
        if(i+1 < argc && !strcmp(argv[i], "as")) {
            if(!read_second) {
                if(root_last) {
                    strncpy(root_last->filename, argv[++i], FILENAME_MAX);
                    root_last->filename[FILENAME_MAX] = '\0';
                }
            }
            else {
                if(second_last) {
                    strncpy(second_last->filename, argv[++i], FILENAME_MAX);
                    second_last->filename[FILENAME_MAX] = '\0';
                }
            }
        }
        else if(i+1 < argc && !strcmp(argv[i], "--min-prefix")) {
            if(active_family == 6) { i++; continue; } /* handled by iprange6_run */
            int j;
            int min_prefix = (int)parse_long_option_or_die("--min-prefix", argv[++i], 1, 32, "It must be between 1 and 32.");
            for(j = 0; j < min_prefix; j++)
                prefix_enabled[j] = 0;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--prefixes")) {
            if(active_family == 6) { i++; continue; } /* handled by iprange6_run */
            char *s = NULL, *e = argv[++i];
            int j;

            for(j = 0; j < 32; j++)
                prefix_enabled[j] = 0;

            while(e && *e && e != s) {
                s = e;
                j = (int)strtol(s, &e, 10);
                if(j <= 0 || j > 32) {
                    fprintf(stderr, "%s: Only prefixes from 1 to 32 can be set (32 is always enabled). %d is invalid.\n", PROG, j);
                    exit(1);
                }
                if(debug) fprintf(stderr, "Enabling prefix %d\n", j);
                prefix_enabled[j] = 1;
                if(*e == ',' || *e == ' ') e++;
            }

            if(e && *e) {
                fprintf(stderr, "%s: Invalid prefix '%s'\n", PROG, e);
                exit(1);
            }
        }
        else if(i+1 < argc && (
               !strcmp(argv[i], "--default-prefix")
            || !strcmp(argv[i], "-p")
            )) {
            if(active_family == 6) { i++; continue; } /* IPv6 always uses 128 as default */
            const char *option = argv[i];
            const char *value = argv[++i];
            default_prefix = (int)parse_long_option_or_die(option, value, 0, 32, "It must be between 0 and 32.");
        }
        else if(i+1 < argc && (
               !strcmp(argv[i], "--ipset-reduce")
            || !strcmp(argv[i], "--reduce-factor")
            )) {
            const char *option = argv[i];
            const char *value = argv[++i];
            ipset_reduce_factor = 100 + parse_size_option_or_die(option, value, 0, SIZE_MAX - 100, "It must be a non-negative integer percentage.");
            mode = MODE_REDUCE;
        }
        else if(i+1 < argc && (
               !strcmp(argv[i], "--ipset-reduce-entries")
            || !strcmp(argv[i], "--reduce-entries")
            )) {
            const char *option = argv[i];
            const char *value = argv[++i];
            ipset_reduce_min_accepted = parse_size_option_or_die(option, value, 0, SIZE_MAX, "It must be a non-negative integer.");
            mode = MODE_REDUCE;
        }
        else if(!strcmp(argv[i], "--ipv4")
            || !strcmp(argv[i], "-4")) {
            active_family = 4;
        }
        else if(!strcmp(argv[i], "--ipv6")
            || !strcmp(argv[i], "-6")) {
            active_family = 6;
        }
        else if(!strcmp(argv[i], "--optimize")
            || !strcmp(argv[i], "--combine")
            || !strcmp(argv[i], "--merge")
            || !strcmp(argv[i], "--union")
            || !strcmp(argv[i], "--union-all")
            || !strcmp(argv[i], "-J")
            ) {
            mode = MODE_COMBINE;
        }
        else if(!strcmp(argv[i], "--common")
            || !strcmp(argv[i], "--intersect")
            || !strcmp(argv[i], "--intersect-all")) {
            mode = MODE_COMMON;
        }
        else if(!strcmp(argv[i], "--exclude-next")
            || !strcmp(argv[i], "--except")
            || !strcmp(argv[i], "--complement-next")
            || !strcmp(argv[i], "--complement")) {
            mode = MODE_EXCLUDE_NEXT;
            read_second = 1;
            if(active_family != 6 && !root) {
                fprintf(stderr, "%s: An ipset is needed before --except\n", PROG);
                exit(1);
            }
        }
        else if(!strcmp(argv[i], "--diff")
            || !strcmp(argv[i], "--diff-next")) {
            mode = MODE_DIFF;
            read_second = 1;
            if(active_family != 6 && !root) {
                fprintf(stderr, "%s: An ipset is needed before --diff\n", PROG);
                exit(1);
            }
        }
        else if(!strcmp(argv[i], "--compare")) {
            mode = MODE_COMPARE;
        }
        else if(!strcmp(argv[i], "--compare-first")) {
            mode = MODE_COMPARE_FIRST;
        }
        else if(!strcmp(argv[i], "--compare-next")) {
            mode = MODE_COMPARE_NEXT;
            read_second = 1;
            if(active_family != 6 && !root) {
                fprintf(stderr, "%s: An ipset is needed before --compare-next\n", PROG);
                exit(1);
            }
        }
        else if(!strcmp(argv[i], "--count-unique")
            || !strcmp(argv[i], "-C")) {
            mode = MODE_COUNT_UNIQUE_MERGED;
        }
        else if(!strcmp(argv[i], "--count-unique-all")) {
            mode = MODE_COUNT_UNIQUE_ALL;
        }
/*
        else if(!strcmp(argv[i], "--histogram")) {
            mode = MODE_HISTOGRAM;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--histogram-dir")) {
            mode = MODE_HISTOGRAM;
            strncpy(histogram_dir, argv[++i], FILENAME_MAX);
        }
*/
        else if(!strcmp(argv[i], "--version")) {
            version();
        }
        else if(!strcmp(argv[i], "--help")
            || !strcmp(argv[i], "-h")) {
            usage(argv[0]);
        }
        else if(!strcmp(argv[i], "-v")) {
            debug = 1;
        }
        else if(!strcmp(argv[i], "--print-ranges")
            || !strcmp(argv[i], "-j")) {
            print = PRINT_RANGE;
        }
        else if(!strcmp(argv[i], "--print-binary")) {
            print = PRINT_BINARY;
        }
        else if(!strcmp(argv[i], "--print-single-ips")
            || !strcmp(argv[i], "-1")) {
            print = PRINT_SINGLE_IPS;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-prefix")) {
            print_prefix_ips  = argv[++i];
            print_prefix_nets = print_prefix_ips;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-prefix-ips")) {
            print_prefix_ips = argv[++i];
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-prefix-nets")) {
            print_prefix_nets = argv[++i];
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-suffix")) {
            print_suffix_ips = argv[++i];
            print_suffix_nets = print_suffix_ips;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-suffix-ips")) {
            print_suffix_ips = argv[++i];
        }
        else if(i+1 < argc && !strcmp(argv[i], "--print-suffix-nets")) {
            print_suffix_nets = argv[++i];
        }
        else if(!strcmp(argv[i], "--header")) {
            header = 1;
        }
        else if(!strcmp(argv[i], "--quiet")) {
            quiet = 1;
        }
        else if(!strcmp(argv[i], "--dont-fix-network")) {
            cidr_use_network = 0;
        }
        else if(i+1 < argc && !strcmp(argv[i], "--dns-threads")) {
            dns_threads_max = (int)parse_long_option_or_die("--dns-threads", argv[++i], 1, INT_MAX, "It must be an integer greater than or equal to 1.");
        }
        else if(!strcmp(argv[i], "--dns-silent")) {
            dns_silent = 1;
        }
        else if(!strcmp(argv[i], "--dns-progress")) {
            dns_progress = 1;
        }
        else if(!strcmp(argv[i], "--has-compare")
            || !strcmp(argv[i], "--has-reduce")) {
            fprintf(stderr, "yes, compare and reduce is present.\n");
            exit(0);
        }
        else if(!strcmp(argv[i], "--has-filelist-loading")
            || !strcmp(argv[i], "--has-directory-loading")) {
            fprintf(stderr, "yes, @filename and @directory support is present.\n");
            exit(0);
        }
        else if(!strcmp(argv[i], "--has-ipv6")) {
            fprintf(stderr, "yes, IPv6 support is present.\n");
            exit(0);
        }
        else {
            /* In IPv6 mode, skip IPv4 loading — iprange6_run() handles it */
            if(active_family == 6) {
                /* still need to handle 'as NAME' and positional state, but skip loading */
                if(strcmp(argv[i], "-") != 0 && argv[i][0] != '@') {
                    /* skip 'as NAME' after regular files */
                    if(i+1 < argc && !strcmp(argv[i+1], "as") && i+2 < argc)
                        i += 2;
                }
                inputs++;
                continue;
            }

            if(!strcmp(argv[i], "-")) {
                inputs++;
                if(!(ips = ipset_load(NULL))) {
                    fprintf(stderr, "%s: Cannot load ipset from stdin\n", PROG);
                    exit(1);
                }

                if(read_second)
                    ipset_chain_append(&second, &second_last, ips);
                else {
                    if(!first) first = ips;
                    ipset_chain_append(&root, &root_last, ips);
                }
            }
            else if(argv[i][0] == '@') {
                inputs++;

                /* Handle @filename as a file list or directory */
                const char *listname = argv[i] + 1;  /* Skip the @ character */
                struct stat st;
                
                if(stat(listname, &st) != 0) {
                    fprintf(stderr, "%s: Cannot access %s: %s\n", PROG, listname, strerror(errno));
                    exit(1);
                }
                
                /* Check if it's a directory */
                if(S_ISDIR(st.st_mode)) {
                    DIR *dir;
                    struct dirent *entry;
                    char **files = NULL;
                    size_t files_allocated = 0, files_collected = 0, j;

                    if(unlikely(debug)) 
                        fprintf(stderr, "%s: Loading files from directory %s\n", PROG, listname);
                    
                    dir = opendir(listname);
                    if(!dir) {
                        fprintf(stderr, "%s: Cannot open directory: %s - %s\n", PROG, listname, strerror(errno));
                        exit(1);
                    }

                    /* Read all files from the directory */
                    while((entry = readdir(dir))) {
                        /* Skip "." and ".." */
                        if(!strcmp(entry->d_name, ".") || !strcmp(entry->d_name, ".."))
                            continue;
                        
                        /* Create full path */
                        char filepath[FILENAME_MAX + 1];
                        snprintf(filepath, FILENAME_MAX, "%s/%s", listname, entry->d_name);
                        
                        /* Auto-discovery should only consume regular files. */
                        if(stat(filepath, &st) != 0 || !S_ISREG(st.st_mode))
                            continue;

                        if(files_collected == files_allocated) {
                            size_t next_allocated = (files_allocated)?files_allocated * 2:16;
                            char **tmp = realloc(files, next_allocated * sizeof(*files));

                            if(!tmp) {
                                closedir(dir);
                                free_pathnames(files, files_collected);
                                fprintf(stderr, "%s: Cannot allocate memory for directory listing %s\n", PROG, listname);
                                exit(1);
                            }

                            files = tmp;
                            files_allocated = next_allocated;
                        }

                        files[files_collected] = strdup(filepath);
                        if(!files[files_collected]) {
                            closedir(dir);
                            free_pathnames(files, files_collected);
                            fprintf(stderr, "%s: Cannot allocate memory for directory entry %s\n", PROG, filepath);
                            exit(1);
                        }

                        files_collected++;
                    }
                    
                    closedir(dir);
                    
                    /* Handle empty directory case */
                    if(!files_collected) {
                        free(files);
                        if(unlikely(debug))
                            fprintf(stderr, "%s: Directory %s is empty or contains no valid files\n", PROG, listname);
                        
                        /* Report an error for empty directory */
                        fprintf(stderr, "%s: No valid files found in directory: %s\n", PROG, listname);
                        exit(1);
                    }

                    qsort(files, files_collected, sizeof(*files), compare_pathnames);

                    for(j = 0; j < files_collected; j++) {
                        if(unlikely(debug))
                            fprintf(stderr, "%s: Loading file %s from directory %s\n", PROG, files[j], listname);

                        if(!(ips = ipset_load(files[j]))) {
                            fprintf(stderr, "%s: Cannot load file %s from directory %s\n",
                                    PROG, files[j], listname);
                            free_pathnames(files, files_collected);
                            exit(1);
                        }

                        if(read_second)
                            ipset_chain_append(&second, &second_last, ips);
                        else {
                            if(!first) first = ips;
                            ipset_chain_append(&root, &root_last, ips);
                        }
                    }

                    free_pathnames(files, files_collected);
                }
                else {
                    /* Handle as a file list */
                    FILE *fp;
                    char line[MAX_LINE + 1];
                    int lineid = 0;
                    
                    if(unlikely(debug)) 
                        fprintf(stderr, "%s: Loading files from list %s\n", PROG, listname);
                    
                    fp = fopen(listname, "r");
                    if(!fp) {
                        fprintf(stderr, "%s: Cannot open file list: %s - %s\n", PROG, listname, strerror(errno));
                        exit(1);
                    }
                    
                    /* Flag to track if we loaded any files */
                    int files_loaded = 0;

                    /* Read each line and load the corresponding file */
                    while(fgets(line, MAX_LINE, fp)) {
                        lineid++;
                        
                        /* Skip empty lines and comments */
                        char *s = line;
                        while(*s && (*s == ' ' || *s == '\t')) s++;
                        if(*s == '\n' || *s == '\r' || *s == '\0' || *s == '#' || *s == ';')
                            continue;
                            
                        /* Remove trailing newlines/whitespace */
                        char *end = s + strlen(s) - 1;
                        while(end > s && (*end == '\n' || *end == '\r' || *end == ' ' || *end == '\t'))
                            *end-- = '\0';
                            
                        if(unlikely(debug)) 
                            fprintf(stderr, "%s: Loading file %s from list (line %d)\n", PROG, s, lineid);
                        
                        /* Load the file as an independent ipset */
                        if(!(ips = ipset_load(s))) {
                            fprintf(stderr, "%s: Cannot load file %s from list %s (line %d)\n", 
                                    PROG, s, listname, lineid);
                            exit(1);
                        }
                        
                        files_loaded = 1;
                        
                        /* Add the ipset to the appropriate chain */
                        if(read_second)
                            ipset_chain_append(&second, &second_last, ips);
                        else {
                            if(!first) first = ips;
                            ipset_chain_append(&root, &root_last, ips);
                        }
                    }
                    
                    fclose(fp);
                    
                    /* Handle empty file list case */
                    if(!files_loaded) {
                        if(unlikely(debug))
                            fprintf(stderr, "%s: File list %s is empty or contains no valid entries\n", PROG, listname);
                        
                        /* Report an error for empty file list */
                        fprintf(stderr, "%s: No valid files found in file list: %s\n", PROG, listname);
                        exit(1);
                    }
                }
            }
            else {
                inputs++;
                if(!(ips = ipset_load(argv[i]))) {
                    fprintf(stderr, "%s: Cannot load ipset: %s\n", PROG, argv[i]);
                    exit(1);
                }
                
                if(read_second)
                    ipset_chain_append(&second, &second_last, ips);
                else {
                    if(!first) first = ips;
                    ipset_chain_append(&root, &root_last, ips);
                }
            }
        }
    }

    /* IPv6 mode: delegate to the IPv6 execution path */
    if(active_family == 6) {
        gettimeofday(&load_dt, NULL);
        ret = iprange6_run(argc, argv, mode, print, header, quiet,
                           ipset_reduce_factor, ipset_reduce_min_accepted);
        exit(ret);
    }

    /*
     * if no ipset was given on the command line
     * assume stdin, regardless of whether other options were specified
     */

    if(!inputs) {
        if(unlikely(debug))
            fprintf(stderr, "%s: No input files provided, reading from stdin\n", PROG);

        if(!(first = root = ipset_load(NULL))) {
            fprintf(stderr, "%s: Cannot load ipset from stdin\n", PROG);
            exit(1);
        }
        root_last = root;
    }

    if(!root) {
        // impossible situation since we fail if no ipset is loaded
        fprintf(stderr, "%s: No valid ipsets to merge from the provided inputs.\n", PROG);
        exit(1);
    }

    gettimeofday(&load_dt, NULL);

        if(mode == MODE_COMBINE || mode == MODE_REDUCE || mode == MODE_COUNT_UNIQUE_MERGED) {
        /* for debug mode to show something meaningful */
        strcpy(root->filename, "combined ipset");

        for(ips = root->next; ips ;ips = ips->next)
            if(unlikely(ipset_merge(root, ips))) {
                fprintf(stderr, "%s: Cannot merge ipset %s into %s\n", PROG, ips->filename, root->filename);
                exit(1);
            }

        /* ipset_optimize(root); */
        if(mode == MODE_REDUCE) ipset_reduce(root, ipset_reduce_factor, ipset_reduce_min_accepted);

        gettimeofday(&print_dt, NULL);

        if(mode == MODE_COMBINE || mode == MODE_REDUCE)
            ipset_print(root, print);

        else if(mode == MODE_COUNT_UNIQUE_MERGED) {
            uint64_t unique_ips = ipset_report_unique_ips(root, NULL);
            if(unlikely(header)) printf("entries,unique_ips\n");
            printf("%zu,%" PRIu64 "\n", root->entries, unique_ips);
        }
    }
    else if(mode == MODE_COMMON) {
        ipset *common = NULL, *ips2 = NULL;

        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to be compared to find their common IPs.\n", PROG);
            exit(1);
        }

        /* ipset_optimize_all(root); */

        common = ipset_common(root, root->next);
        for(ips = root->next->next; ips ;ips = ips->next) {
            ips2 = ipset_common(common, ips);
            ipset_free(common);
            common = ips2;
        }

        gettimeofday(&print_dt, NULL);
        ipset_print(common, print);
    }
    else if(mode == MODE_DIFF) {
        if(!root || !second) {
            fprintf(stderr, "%s: two ipsets at least are needed to be diffed.\n", PROG);
            exit(1);
        }

        for(ips = root->next; ips ;ips = ips->next)
            if(unlikely(ipset_merge(root, ips))) {
                fprintf(stderr, "%s: Cannot merge ipset %s into %s\n", PROG, ips->filename, root->filename);
                exit(1);
            }
        if(root->next) strcpy(root->filename, "ipset A");

        for(ips = second->next; ips ;ips = ips->next)
            if(unlikely(ipset_merge(second, ips))) {
                fprintf(stderr, "%s: Cannot merge ipset %s into %s\n", PROG, ips->filename, second->filename);
                exit(1);
            }
        if(second->next) strcpy(second->filename, "ipset B");

        ips = ipset_diff(root, second);

        gettimeofday(&print_dt, NULL);
        if(!quiet) ipset_print(ips, print);
        gettimeofday(&print_dt, NULL);

        if(ips->unique_ips) ret = 1;
        else ret = 0;
    }
    else if(mode == MODE_COMPARE) {
        ipset *ips2;

        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to be compared.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n");

        ipset_optimize_all(root);

        for(ips = root; ips ;ips = ips->next) {
            for(ips2 = ips; ips2 ;ips2 = ips2->next) {
                ipset *comips;
                size_t entries1, entries2;
                uint64_t unique1 = ipset_report_unique_ips(ips, &entries1);
                uint64_t unique2 = ipset_report_unique_ips(ips2, &entries2);

                if(ips == ips2) continue;

#ifdef COMPARE_WITH_COMMON
                comips = ipset_common(ips, ips2);
                if(!comips) {
                    fprintf(stderr, "%s: Cannot find the common IPs of ipset %s and %s\n", PROG, ips->filename, ips2->filename);
                    exit(1);
                }
                fprintf(stdout, "%s,%s,%zu,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n", ips->filename, ips2->filename, entries1, entries2, unique1, unique2, unique1 + unique2 - comips->unique_ips, comips->unique_ips);
                ipset_free(comips);
#else
                comips = ipset_combine(ips, ips2);
                if(!comips) {
                    fprintf(stderr, "%s: Cannot merge ipset %s and %s\n", PROG, ips->filename, ips2->filename);
                    exit(1);
                }

                ipset_optimize(comips);
                fprintf(stdout, "%s,%s,%zu,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n", ips->filename, ips2->filename, entries1, entries2, unique1, unique2, comips->unique_ips, unique1 + unique2 - comips->unique_ips);
                ipset_free(comips);
#endif
            }
        }
        gettimeofday(&print_dt, NULL);
    }
    else if(mode == MODE_COMPARE_NEXT) {
        ipset *ips2;

        if(!second) {
            fprintf(stderr, "%s: no files given after the --compare-next parameter.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n");

        ipset_optimize_all(root);
        ipset_optimize_all(second);

        for(ips = root; ips ;ips = ips->next) {
            for(ips2 = second; ips2 ;ips2 = ips2->next) {
                size_t entries1, entries2;
                uint64_t unique1 = ipset_report_unique_ips(ips, &entries1);
                uint64_t unique2 = ipset_report_unique_ips(ips2, &entries2);
#ifdef COMPARE_WITH_COMMON
                ipset *common = ipset_common(ips, ips2);
                if(!common) {
                    fprintf(stderr, "%s: Cannot find the common IPs of ipset %s and %s\n", PROG, ips->filename, ips2->filename);
                    exit(1);
                }
                fprintf(stdout, "%s,%s,%zu,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n", ips->filename, ips2->filename, entries1, entries2, unique1, unique2, unique1 + unique2 - common->unique_ips, common->unique_ips);
                ipset_free(common);
#else
                ipset *combined = ipset_combine(ips, ips2);
                if(!combined) {
                    fprintf(stderr, "%s: Cannot merge ipset %s and %s\n", PROG, ips->filename, ips2->filename);
                    exit(1);
                }

                ipset_optimize(combined);
                fprintf(stdout, "%s,%s,%zu,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n", ips->filename, ips2->filename, entries1, entries2, unique1, unique2, combined->unique_ips, unique1 + unique2 - combined->unique_ips);
                ipset_free(combined);
#endif
            }
        }
        gettimeofday(&print_dt, NULL);
    }
    else if(mode == MODE_COMPARE_FIRST) {
        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to be compared.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name,entries,unique_ips,common_ips\n");

        ipset_optimize_all(root);

        for(ips = root; ips ;ips = ips->next) {
            ipset *comips;
            size_t entries;
            uint64_t unique_ips = ipset_report_unique_ips(ips, &entries);

            if(ips == first) continue;

#ifdef COMPARE_WITH_COMMON
            comips = ipset_common(ips, first);
            if(!comips) {
                fprintf(stderr, "%s: Cannot find the comips IPs of ipset %s and %s\n", PROG, ips->filename, first->filename);
                exit(1);
            }
            printf("%s,%zu,%" PRIu64 ",%" PRIu64 "\n", ips->filename, entries, unique_ips, comips->unique_ips);
            ipset_free(comips);
#else
            comips = ipset_combine(ips, first);
            if(!comips) {
                fprintf(stderr, "%s: Cannot merge ipset %s and %s\n", PROG, ips->filename, first->filename);
                exit(1);
            }

            ipset_optimize(comips);
            printf("%s,%zu,%" PRIu64 ",%" PRIu64 "\n", ips->filename, entries, unique_ips, unique_ips + first->unique_ips - comips->unique_ips);
            ipset_free(comips);
#endif
        }
        gettimeofday(&print_dt, NULL);
    }
    else if(mode == MODE_EXCLUDE_NEXT) {
        ipset *excluded;

        if(!second) {
            fprintf(stderr, "%s: no files given after the --exclude-next parameter.\n", PROG);
            exit(1);
        }

        /* merge them */
        for(ips = root->next; ips ;ips = ips->next)
            if(unlikely(ipset_merge(root, ips))) {
                fprintf(stderr, "%s: Cannot merge ipset %s into %s\n", PROG, ips->filename, root->filename);
                exit(1);
            }

        /* ipset_optimize(root); */
        /* ipset_optimize_all(second); */

        excluded = root;
        root = root->next;
        for(ips = second; ips ;ips = ips->next) {
            ipset *tmp = ipset_exclude(excluded, ips);
            if(!tmp) {
                fprintf(stderr, "%s: Cannot exclude the IPs of ipset %s from %s\n", PROG, ips->filename, excluded->filename);
                exit(1);
            }

            ipset_free(excluded);
            excluded = tmp;
        }
        gettimeofday(&print_dt, NULL);
        ipset_print(excluded, print);
    }
    else if(mode == MODE_COUNT_UNIQUE_ALL) {
        if(unlikely(header)) printf("name,entries,unique_ips\n");

        ipset_optimize_all(root);

        for(ips = root; ips ;ips = ips->next) {
            printf("%s,%zu,%" PRIu64 "\n", ips->filename, ips->entries, ips->unique_ips);
        }
        gettimeofday(&print_dt, NULL);
    }
/*
    else if(mode == MODE_HISTOGRAM) {
        for(ips = root; ips ;ips = ips->next) {
            ipset_histogram(ips, histogram_dir);
        }
    }
*/
    else {
        fprintf(stderr, "%s: Unknown mode.\n", PROG);
        exit(1);
    }

    gettimeofday(&stop_dt, NULL);
    if(debug)
        fprintf(stderr, "completed in %0.5f seconds (read %0.5f + think %0.5f + speak %0.5f)\n"
            , ((double)(stop_dt.tv_sec  * 1000000 + stop_dt.tv_usec) - (double)(start_dt.tv_sec * 1000000 + start_dt.tv_usec)) / (double)1000000
            , ((double)(load_dt.tv_sec  * 1000000 + load_dt.tv_usec) - (double)(start_dt.tv_sec * 1000000 + start_dt.tv_usec)) / (double)1000000
            , ((double)(print_dt.tv_sec  * 1000000 + print_dt.tv_usec) - (double)(load_dt.tv_sec * 1000000 + load_dt.tv_usec)) / (double)1000000
            , ((double)(stop_dt.tv_sec  * 1000000 + stop_dt.tv_usec) - (double)(print_dt.tv_sec * 1000000 + print_dt.tv_usec)) / (double)1000000
        );

    exit(ret);
}
