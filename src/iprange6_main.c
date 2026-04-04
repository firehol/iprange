/*
 * IPv6 mode main execution.
 * Called from iprange.c when active_family == 6.
 * Re-scans argv for file arguments and processes them with ipset6.
 */

#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"
#include "ipset6_print.h"
#include "ipset6_binary.h"
#include "ipset6_load.h"
#include <sys/stat.h>
#include <dirent.h>

extern int active_family;
extern unsigned long ipv6_dropped_in_ipv4_mode;

static void ipset6_chain_append_local(ipset6 **head, ipset6 **tail, ipset6 *ips)
{
    ips->next = NULL;
    ips->prev = *tail;

    if(*tail) (*tail)->next = ips;
    else *head = ips;

    *tail = ips;
}

static int compare_pathnames6(const void *left, const void *right)
{
    const char * const *a = left;
    const char * const *b = right;
    return strcmp(*a, *b);
}

static void free_pathnames6(char **files, size_t entries)
{
    size_t i;
    for(i = 0; i < entries; i++)
        free(files[i]);
    free(files);
}

static __uint128_t ipset6_report_unique_ips(ipset6 *ips, size_t *entries)
{
    __uint128_t unique_ips = ipset6_unique_ips(ips);
    if(entries) *entries = ips->entries;
    return unique_ips;
}

/*
 * iprange6_run() - execute IPv6 mode
 *
 * Parameters are the same state that main() has after option parsing:
 * mode, print format, header flag, quiet flag, etc.
 */
int iprange6_run(int argc, char **argv, int mode, IPSET_PRINT_CMD print,
                 int header, int quiet, size_t ipset_reduce_factor,
                 size_t ipset_reduce_min_accepted)
{
    ipset6 *root = NULL, *root_last = NULL, *ips6 = NULL;
    ipset6 *first = NULL, *second = NULL, *second_last = NULL;
    int i, read_second = 0, inputs = 0, ret = 0;
    char u128buf[40];

    /* re-scan argv for file arguments, positional operators, and IPv6-relevant options */
    for(i = 1; i < argc; i++) {
        /* handle --min-prefix for IPv6 (0..128) */
        if(i+1 < argc && !strcmp(argv[i], "--min-prefix")) {
            int j;
            char *end = NULL;
            long val;
            errno = 0;
            val = strtol(argv[++i], &end, 10);
            if(errno || !end || end == argv[i] || *end != '\0' || val < 1 || val > 128) {
                fprintf(stderr, "%s: Invalid value '%s' for --min-prefix. It must be between 1 and 128.\n", PROG, argv[i]);
                exit(1);
            }
            for(j = 0; j < (int)val; j++)
                prefix6_enabled[j] = 0;
            continue;
        }

        /* handle --prefixes for IPv6 (1..128) */
        if(i+1 < argc && !strcmp(argv[i], "--prefixes")) {
            char *s = NULL, *e = argv[++i];
            int j;
            for(j = 0; j < 128; j++)
                prefix6_enabled[j] = 0;
            while(e && *e && e != s) {
                s = e;
                j = (int)strtol(s, &e, 10);
                if(j <= 0 || j > 128) {
                    fprintf(stderr, "%s: Only prefixes from 1 to 128 can be set. %d is invalid.\n", PROG, j);
                    exit(1);
                }
                prefix6_enabled[j] = 1;
                if(*e == ',' || *e == ' ') e++;
            }
            if(e && *e) {
                fprintf(stderr, "%s: Invalid prefix '%s'\n", PROG, e);
                exit(1);
            }
            continue;
        }

        /* handle --default-prefix for IPv6 (0..128) */
        if(i+1 < argc && (!strcmp(argv[i], "--default-prefix") || !strcmp(argv[i], "-p"))) {
            /* already parsed in main() for IPv4 range (0..32); we just skip the value here
             * since the IPv6 parser always uses 128 as default prefix */
            i++;
            continue;
        }

        /* skip options that take a value */
        if(i+1 < argc && (!strcmp(argv[i], "as")
            || !strcmp(argv[i], "--ipset-reduce") || !strcmp(argv[i], "--reduce-factor")
            || !strcmp(argv[i], "--ipset-reduce-entries") || !strcmp(argv[i], "--reduce-entries")
            || !strcmp(argv[i], "--print-prefix")
            || !strcmp(argv[i], "--print-prefix-ips")
            || !strcmp(argv[i], "--print-prefix-nets")
            || !strcmp(argv[i], "--print-suffix")
            || !strcmp(argv[i], "--print-suffix-ips")
            || !strcmp(argv[i], "--print-suffix-nets")
            || !strcmp(argv[i], "--dns-threads")
            )) {
            i++; /* skip value */
            continue;
        }

        /* skip known flags */
        if(argv[i][0] == '-' && argv[i][1] != '\0' && strcmp(argv[i], "-")) {
            /* handle positional operators */
            if(!strcmp(argv[i], "--exclude-next") || !strcmp(argv[i], "--except")
               || !strcmp(argv[i], "--complement-next") || !strcmp(argv[i], "--complement")) {
                read_second = 1;
                continue;
            }
            if(!strcmp(argv[i], "--diff") || !strcmp(argv[i], "--diff-next")) {
                read_second = 1;
                continue;
            }
            if(!strcmp(argv[i], "--compare-next")) {
                read_second = 1;
                continue;
            }
            /* all other flags: skip */
            continue;
        }

        /* this is a file argument (or "-" for stdin) */
        inputs++;

        if(!strcmp(argv[i], "-")) {
            if(!(ips6 = ipset6_load(NULL))) {
                fprintf(stderr, "%s: Cannot load ipset from stdin\n", PROG);
                exit(1);
            }
        }
        else if(argv[i][0] == '@') {
            const char *listname = argv[i] + 1;
            struct stat st;

            if(stat(listname, &st) != 0) {
                fprintf(stderr, "%s: Cannot access %s: %s\n", PROG, listname, strerror(errno));
                exit(1);
            }

            if(S_ISDIR(st.st_mode)) {
                DIR *dir;
                struct dirent *entry;
                char **files = NULL;
                size_t files_allocated = 0, files_collected = 0, j;

                dir = opendir(listname);
                if(!dir) {
                    fprintf(stderr, "%s: Cannot open directory: %s - %s\n", PROG, listname, strerror(errno));
                    exit(1);
                }

                while((entry = readdir(dir))) {
                    if(!strcmp(entry->d_name, ".") || !strcmp(entry->d_name, ".."))
                        continue;

                    char filepath[FILENAME_MAX + 1];
                    snprintf(filepath, FILENAME_MAX, "%s/%s", listname, entry->d_name);

                    if(stat(filepath, &st) != 0 || !S_ISREG(st.st_mode))
                        continue;

                    if(files_collected == files_allocated) {
                        size_t next_allocated = files_allocated ? files_allocated * 2 : 16;
                        char **tmp = realloc(files, next_allocated * sizeof(*files));
                        if(!tmp) {
                            closedir(dir);
                            free_pathnames6(files, files_collected);
                            fprintf(stderr, "%s: Cannot allocate memory\n", PROG);
                            exit(1);
                        }
                        files = tmp;
                        files_allocated = next_allocated;
                    }

                    files[files_collected] = strdup(filepath);
                    if(!files[files_collected]) {
                        closedir(dir);
                        free_pathnames6(files, files_collected);
                        fprintf(stderr, "%s: Cannot allocate memory\n", PROG);
                        exit(1);
                    }
                    files_collected++;
                }
                closedir(dir);

                if(!files_collected) {
                    free(files);
                    fprintf(stderr, "%s: No valid files found in directory: %s\n", PROG, listname);
                    exit(1);
                }

                qsort(files, files_collected, sizeof(*files), compare_pathnames6);

                for(j = 0; j < files_collected; j++) {
                    if(!(ips6 = ipset6_load(files[j]))) {
                        fprintf(stderr, "%s: Cannot load file %s\n", PROG, files[j]);
                        free_pathnames6(files, files_collected);
                        exit(1);
                    }

                    if(read_second)
                        ipset6_chain_append_local(&second, &second_last, ips6);
                    else {
                        if(!first) first = ips6;
                        ipset6_chain_append_local(&root, &root_last, ips6);
                    }
                }
                free_pathnames6(files, files_collected);
                continue;
            }
            else {
                /* file list */
                FILE *fp = fopen(listname, "r");
                char line[MAX_LINE + 1];
                int lineid = 0, files_loaded = 0;

                if(!fp) {
                    fprintf(stderr, "%s: Cannot open file list: %s - %s\n", PROG, listname, strerror(errno));
                    exit(1);
                }

                while(fgets(line, MAX_LINE, fp)) {
                    lineid++;
                    char *s = line;
                    while(*s == ' ' || *s == '\t') s++;
                    if(*s == '\n' || *s == '\r' || *s == '\0' || *s == '#' || *s == ';')
                        continue;
                    char *end = s + strlen(s) - 1;
                    while(end > s && (*end == '\n' || *end == '\r' || *end == ' ' || *end == '\t'))
                        *end-- = '\0';

                    if(!(ips6 = ipset6_load(s))) {
                        fprintf(stderr, "%s: Cannot load file %s from list %s (line %d)\n", PROG, s, listname, lineid);
                        fclose(fp);
                        exit(1);
                    }
                    files_loaded = 1;

                    if(read_second)
                        ipset6_chain_append_local(&second, &second_last, ips6);
                    else {
                        if(!first) first = ips6;
                        ipset6_chain_append_local(&root, &root_last, ips6);
                    }
                }
                fclose(fp);

                if(!files_loaded) {
                    fprintf(stderr, "%s: No valid files found in file list: %s\n", PROG, listname);
                    exit(1);
                }
                continue;
            }
        }
        else {
            if(!(ips6 = ipset6_load(argv[i]))) {
                fprintf(stderr, "%s: Cannot load ipset: %s\n", PROG, argv[i]);
                exit(1);
            }
        }

        /* handle 'as NAME' */
        if(i+1 < argc && !strcmp(argv[i+1], "as") && i+2 < argc) {
            strncpy(ips6->filename, argv[i+2], FILENAME_MAX);
            ips6->filename[FILENAME_MAX] = '\0';
            i += 2;
        }

        if(read_second)
            ipset6_chain_append_local(&second, &second_last, ips6);
        else {
            if(!first) first = ips6;
            ipset6_chain_append_local(&root, &root_last, ips6);
        }
    }

    /* if no files given, read from stdin */
    if(!inputs) {
        if(!(first = root = ipset6_load(NULL))) {
            fprintf(stderr, "%s: Cannot load ipset from stdin\n", PROG);
            exit(1);
        }
        root_last = root;
    }

    if(!root) {
        fprintf(stderr, "%s: No valid ipsets to process.\n", PROG);
        exit(1);
    }

    /* --- mode execution (mirrors the IPv4 logic in main()) --- */

    if(mode == MODE_COMBINE || mode == MODE_REDUCE || mode == MODE_COUNT_UNIQUE_MERGED) {
        strcpy(root->filename, "combined ipset");

        for(ips6 = root->next; ips6; ips6 = ips6->next)
            if(unlikely(ipset6_merge(root, ips6))) {
                fprintf(stderr, "%s: Cannot merge ipset %s\n", PROG, ips6->filename);
                exit(1);
            }

        if(mode == MODE_REDUCE) {
            fprintf(stderr, "%s: --ipset-reduce is not supported in IPv6 mode\n", PROG);
            exit(1);
        }

        if(mode == MODE_COMBINE)
            ipset6_print(root, print);
        else if(mode == MODE_COUNT_UNIQUE_MERGED) {
            __uint128_t unique_ips = ipset6_report_unique_ips(root, NULL);
            if(unlikely(header)) printf("entries,unique_ips\n");
            printf("%zu,%s\n", root->entries, u128_to_dec(u128buf, sizeof(u128buf), unique_ips));
        }
    }
    else if(mode == MODE_COMMON) {
        ipset6 *common = NULL, *ips2 = NULL;

        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to find common IPs.\n", PROG);
            exit(1);
        }

        common = ipset6_common(root, root->next);
        for(ips6 = root->next->next; ips6; ips6 = ips6->next) {
            ips2 = ipset6_common(common, ips6);
            ipset6_free(common);
            common = ips2;
        }
        ipset6_print(common, print);
    }
    else if(mode == MODE_DIFF) {
        if(!root || !second) {
            fprintf(stderr, "%s: two ipsets at least are needed to be diffed.\n", PROG);
            exit(1);
        }

        for(ips6 = root->next; ips6; ips6 = ips6->next)
            if(unlikely(ipset6_merge(root, ips6))) {
                fprintf(stderr, "%s: Cannot merge ipset %s\n", PROG, ips6->filename);
                exit(1);
            }
        if(root->next) strcpy(root->filename, "ipset A");

        for(ips6 = second->next; ips6; ips6 = ips6->next)
            if(unlikely(ipset6_merge(second, ips6))) {
                fprintf(stderr, "%s: Cannot merge ipset %s\n", PROG, ips6->filename);
                exit(1);
            }
        if(second->next) strcpy(second->filename, "ipset B");

        ips6 = ipset6_diff(root, second);
        if(!quiet) ipset6_print(ips6, print);

        if(ips6->unique_ips) ret = 1;
        else ret = 0;
    }
    else if(mode == MODE_COMPARE) {
        ipset6 *ips2;

        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to be compared.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n");

        ipset6_optimize_all(root);

        for(ips6 = root; ips6; ips6 = ips6->next) {
            for(ips2 = ips6; ips2; ips2 = ips2->next) {
                ipset6 *comips;
                size_t entries1, entries2;
                __uint128_t unique1 = ipset6_report_unique_ips(ips6, &entries1);
                __uint128_t unique2 = ipset6_report_unique_ips(ips2, &entries2);

                if(ips6 == ips2) continue;

                comips = ipset6_combine(ips6, ips2);
                if(!comips) {
                    fprintf(stderr, "%s: Cannot merge ipsets\n", PROG);
                    exit(1);
                }

                ipset6_optimize(comips);
                printf("%s,%s,%zu,%zu,%s,", ips6->filename, ips2->filename, entries1, entries2,
                    u128_to_dec(u128buf, sizeof(u128buf), unique1));
                printf("%s,", u128_to_dec(u128buf, sizeof(u128buf), unique2));
                printf("%s,", u128_to_dec(u128buf, sizeof(u128buf), comips->unique_ips));
                printf("%s\n", u128_to_dec(u128buf, sizeof(u128buf), unique1 + unique2 - comips->unique_ips));
                ipset6_free(comips);
            }
        }
    }
    else if(mode == MODE_COMPARE_NEXT) {
        ipset6 *ips2;

        if(!second) {
            fprintf(stderr, "%s: no files given after the --compare-next parameter.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips\n");

        ipset6_optimize_all(root);
        ipset6_optimize_all(second);

        for(ips6 = root; ips6; ips6 = ips6->next) {
            for(ips2 = second; ips2; ips2 = ips2->next) {
                size_t entries1, entries2;
                __uint128_t unique1 = ipset6_report_unique_ips(ips6, &entries1);
                __uint128_t unique2 = ipset6_report_unique_ips(ips2, &entries2);

                ipset6 *combined = ipset6_combine(ips6, ips2);
                if(!combined) {
                    fprintf(stderr, "%s: Cannot merge ipsets\n", PROG);
                    exit(1);
                }

                ipset6_optimize(combined);
                printf("%s,%s,%zu,%zu,%s,", ips6->filename, ips2->filename, entries1, entries2,
                    u128_to_dec(u128buf, sizeof(u128buf), unique1));
                printf("%s,", u128_to_dec(u128buf, sizeof(u128buf), unique2));
                printf("%s,", u128_to_dec(u128buf, sizeof(u128buf), combined->unique_ips));
                printf("%s\n", u128_to_dec(u128buf, sizeof(u128buf), unique1 + unique2 - combined->unique_ips));
                ipset6_free(combined);
            }
        }
    }
    else if(mode == MODE_COMPARE_FIRST) {
        if(!root->next) {
            fprintf(stderr, "%s: two ipsets at least are needed to be compared.\n", PROG);
            exit(1);
        }

        if(unlikely(header)) printf("name,entries,unique_ips,common_ips\n");

        ipset6_optimize_all(root);

        for(ips6 = root; ips6; ips6 = ips6->next) {
            size_t entries;
            __uint128_t unique_ips = ipset6_report_unique_ips(ips6, &entries);

            if(ips6 == first) continue;

            ipset6 *comips = ipset6_combine(ips6, first);
            if(!comips) {
                fprintf(stderr, "%s: Cannot merge ipsets\n", PROG);
                exit(1);
            }

            ipset6_optimize(comips);
            printf("%s,%zu,%s,", ips6->filename, entries,
                u128_to_dec(u128buf, sizeof(u128buf), unique_ips));
            printf("%s\n", u128_to_dec(u128buf, sizeof(u128buf), unique_ips + first->unique_ips - comips->unique_ips));
            ipset6_free(comips);
        }
    }
    else if(mode == MODE_EXCLUDE_NEXT) {
        ipset6 *excluded;

        if(!second) {
            fprintf(stderr, "%s: no files given after the --exclude-next parameter.\n", PROG);
            exit(1);
        }

        for(ips6 = root->next; ips6; ips6 = ips6->next)
            if(unlikely(ipset6_merge(root, ips6))) {
                fprintf(stderr, "%s: Cannot merge ipset %s\n", PROG, ips6->filename);
                exit(1);
            }

        excluded = root;
        for(ips6 = second; ips6; ips6 = ips6->next) {
            ipset6 *tmp = ipset6_exclude(excluded, ips6);
            if(!tmp) {
                fprintf(stderr, "%s: Cannot exclude IPs\n", PROG);
                exit(1);
            }
            if(excluded != root) ipset6_free(excluded);
            excluded = tmp;
        }
        ipset6_print(excluded, print);
    }
    else if(mode == MODE_COUNT_UNIQUE_ALL) {
        if(unlikely(header)) printf("name,entries,unique_ips\n");

        ipset6_optimize_all(root);

        for(ips6 = root; ips6; ips6 = ips6->next) {
            printf("%s,%zu,%s\n", ips6->filename, ips6->entries,
                u128_to_dec(u128buf, sizeof(u128buf), ips6->unique_ips));
        }
    }
    else {
        fprintf(stderr, "%s: Unknown mode.\n", PROG);
        exit(1);
    }

    (void)ipset_reduce_factor;
    (void)ipset_reduce_min_accepted;

    return ret;
}
