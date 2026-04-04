#include "iprange.h"

/*
 * the maximum line element to read in input files
 * normally the elements are IP, IP/MASK, HOSTNAME
 */
#define MAX_INPUT_ELEMENT 255


/* ----------------------------------------------------------------------------
 * parse_line()
 *
 * it parses a single line of input
 * returns
 * 		-1 = cannot parse line
 * 		 0 = skip line - nothing useful here
 * 		 1 = parsed 1 ip address
 * 		 2 = parsed 2 ip addresses
 *       3 = parsed 1 hostname
 *
 */

typedef enum ipset_line_type {
    LINE_IS_INVALID = -1,
    LINE_IS_EMPTY = 0,
    LINE_HAS_1_IP = 1,
    LINE_HAS_2_IPS = 2,
    LINE_HAS_1_HOSTNAME = 3
} IPSET_LINE_TYPE;

static inline int token_looks_ip_like(const char *token)
{
    return (strchr(token, '.') || strchr(token, '/'));
}

static inline int is_hostname_char(char c)
{
    return (
        (c >= '0' && c <= '9')
        || (c >= 'a' && c <= 'z')
        || (c >= 'A' && c <= 'Z')
        || c == '_'
        || c == '-'
        || c == '.'
    );
}

static inline int token_is_complete_ipv4_candidate(const char *token)
{
    int dots = 0;
    int digits_in_part = 0;
    const char *s;

    if(strchr(token, '/')) return 1;

    for(s = token; *s; s++) {
        if(*s >= '0' && *s <= '9') {
            digits_in_part++;
            continue;
        }

        if(*s == '.' && digits_in_part) {
            dots++;
            digits_in_part = 0;
            continue;
        }

        return 0;
    }

    return (dots == 3 && digits_in_part);
}

static inline int line_is_hostname_candidate(const char *line)
{
    const char *s = line;
    int has_chars = 0;

    while(*s == ' ' || *s == '\t') s++;

    while(is_hostname_char(*s)) {
        has_chars = 1;
        s++;
    }

    if(unlikely(!has_chars)) return 0;

    while(*s == ' ' || *s == '\t') s++;

    return (*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0');
}

static inline IPSET_LINE_TYPE parse_hostname(char *line, int lineid, char *ipstr, char *ipstr2, int len) {
    char *s = line;
    int i = 0;

    if(ipstr2 || lineid) { ; }

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    while(likely(i < len && (
            (*s >= '0' && *s <= '9')
            || (*s >= 'a' && *s <= 'z')
            || (*s >= 'A' && *s <= 'Z')
            || *s == '_'
            || *s == '-'
            || *s == '.'
    ))) ipstr[i++] = *s++;

    if(unlikely(!i)) return LINE_IS_INVALID;

    /* terminate ipstr */
    ipstr[i] = '\0';

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    /* the rest is comment */
    if(unlikely(*s == '#' || *s == ';')) return LINE_HAS_1_HOSTNAME;

    /* if we reached the end of line */
    if(likely(*s == '\r' || *s == '\n' || *s == '\0')) return LINE_HAS_1_HOSTNAME;

    return LINE_IS_INVALID;
}

static inline IPSET_LINE_TYPE parse_line(char *line, int lineid, char *ipstr, char *ipstr2, int len) {
    char *s = line;
    int i = 0;
    int ip_like = 0;
    int hostname_candidate = 0;

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    /* skip a line of comment */
    if(unlikely(*s == '#' || *s == ';')) return LINE_IS_EMPTY;

    /* if we reached the end of line */
    if(unlikely(*s == '\r' || *s == '\n' || *s == '\0')) return LINE_IS_EMPTY;

    /* get the ip address */
    while(likely(i < len && ((*s >= '0' && *s <= '9') || *s == '.' || *s == '/')))
        ipstr[i++] = *s++;

    if(unlikely(!i)) return parse_hostname(line, lineid, ipstr, ipstr2, len);

    /* terminate ipstr */
    ipstr[i] = '\0';
    ip_like = token_looks_ip_like(ipstr);
    hostname_candidate = line_is_hostname_candidate(line);

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    /* the rest is comment */
    if(unlikely(*s == '#' || *s == ';')) return LINE_HAS_1_IP;

    /* if we reached the end of line */
    if(likely(*s == '\r' || *s == '\n' || *s == '\0')) return LINE_HAS_1_IP;

    if(unlikely(*s != '-')) {
        if(strchr(ipstr, '/')) return LINE_IS_INVALID;
        if(ip_like && token_is_complete_ipv4_candidate(ipstr)) return LINE_IS_INVALID;
        if(hostname_candidate) return parse_hostname(line, lineid, ipstr, ipstr2, len);
        return LINE_IS_INVALID;
    }

    /* skip the - */
    s++;

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    /* the rest is comment */
    if(unlikely(*s == '#' || *s == ';')) {
        fprintf(stderr, "%s: Ignoring text on line %d, expected an ip address after -, but found '%s'\n", PROG, lineid, s);
        return LINE_HAS_1_IP;
    }

    /* if we reached the end of line */
    if(unlikely(*s == '\r' || *s == '\n' || *s == '\0')) {
        fprintf(stderr, "%s: Incomplete range on line %d, expected an ip address after -, but line ended\n", PROG, lineid);
        return LINE_HAS_1_IP;
    }

    /* get the ip 2nd address */
    i = 0;
    while(likely(i < len && ((*s >= '0' && *s <= '9') || *s == '.' || *s == '/')))
        ipstr2[i++] = *s++;

    if(unlikely(!i)) {
        if(!strchr(ipstr, '/') && !token_is_complete_ipv4_candidate(ipstr) && hostname_candidate)
            return parse_hostname(line, lineid, ipstr, ipstr2, len);
        return LINE_IS_INVALID;
    }

    /* terminate ipstr */
    ipstr2[i] = '\0';

    /* skip all spaces */
    while(unlikely(*s == ' ' || *s == '\t')) s++;

    /* the rest is comment */
    if(unlikely(*s == '#' || *s == ';')) return LINE_HAS_2_IPS;

    /* if we reached the end of line */
    if(likely(*s == '\r' || *s == '\n' || *s == '\0')) return LINE_HAS_2_IPS;

    if(!strchr(ipstr, '/') && !token_is_complete_ipv4_candidate(ipstr) && hostname_candidate)
        return parse_hostname(line, lineid, ipstr, ipstr2, len);

    return LINE_IS_INVALID;
}


/* ----------------------------------------------------------------------------
 * ipset_load()
 *
 * loads a file and stores all entries it finds to a new ipset it creates
 * if the filename is NULL, stdin is used
 *
 * the result is not optimized. To optimize it call ipset_optimize().
 *
 */

ipset *ipset_load(const char *filename) {
    FILE *fp = stdin;
    int lineid = 0;
    int parse_errors = 0;
    char line[MAX_LINE + 1], ipstr[MAX_INPUT_ELEMENT + 1], ipstr2[MAX_INPUT_ELEMENT + 1];
    ipset *ips = ipset_create((filename && *filename)?filename:"stdin", 0);

    if(unlikely(!ips)) return NULL;

    if (likely(filename && *filename)) {
        fp = fopen(filename, "r");
        if (unlikely(!fp)) {
            fprintf(stderr, "%s: %s - %s\n", PROG, filename, strerror(errno));
            ipset_free(ips);
            return NULL;
        }
    }

    /* load it */
    if(unlikely(debug)) fprintf(stderr, "%s: Loading from %s\n", PROG, ips->filename);

    /* it will be removed, if the loaded ipset is not optimized on disk */
    ips->flags |= IPSET_FLAG_OPTIMIZED;

    if(!fgets(line, MAX_LINE, fp)) {
        if(likely(fp != stdin))
            fclose(fp);

        /* For normal files, an empty file is valid too (return empty ipset) */
        if(unlikely(debug))
            fprintf(stderr, "%s: %s is empty\n", PROG, filename && *filename?filename:"stdin");

        return ips;
    }

    if(unlikely(!strcmp(line, BINARY_HEADER_V10))) {
        if(ipset_load_binary_v10(fp, ips, 1)) {
            fprintf(stderr, "%s: Cannot fast load %s\n", PROG, filename);
            ipset_free(ips);
            ips = NULL;
        }

        if(likely(fp != stdin)) fclose(fp);
        if(unlikely(debug)) if(ips) fprintf(stderr, "%s: Binary loaded %s %s\n", PROG, (ips->flags & IPSET_FLAG_OPTIMIZED)?"optimized":"non-optimized", ips->filename);

        return ips;
    }

    do {
        lineid++;

        switch(parse_line(line, lineid, ipstr, ipstr2, MAX_INPUT_ELEMENT)) {
            case LINE_IS_INVALID:
                /* cannot read line */
                fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                parse_errors = 1;
                break;

            case LINE_IS_EMPTY:
                /* nothing on this line */
                break;

            case LINE_HAS_1_IP:
                /* 1 IP on this line */
                if(unlikely(!ipset_add_ipstr(ips, ipstr))) {
                    fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                    parse_errors = 1;
                }
                break;

            case LINE_HAS_2_IPS:
                /* 2 IPs in range on this line */
            {
                int err = 0;
                in_addr_t lo, hi;
                network_addr_t netaddr1, netaddr2;
                netaddr1 = str2netaddr(ipstr, &err);
                if(likely(!err)) netaddr2 = str2netaddr(ipstr2, &err);
                if(unlikely(err)) {
                    fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                    parse_errors = 1;
                    continue;
                }

                lo = (netaddr1.addr < netaddr2.addr)?netaddr1.addr:netaddr2.addr;
                hi = (netaddr1.broadcast > netaddr2.broadcast)?netaddr1.broadcast:netaddr2.broadcast;
                ipset_add_ip_range(ips, lo, hi);
            }
                break;

            case LINE_HAS_1_HOSTNAME:
                if(unlikely(debug))
                    fprintf(stderr, "%s: DNS resolution for hostname '%s' from line %d of file %s.\n", PROG, ipstr, lineid, ips->filename);

                /* resolve_hostname(ips, ipstr); */
                if(unlikely(dns_request(ips, ipstr))) {
                    if(likely(fp != stdin)) fclose(fp);
                    dns_reset_stats();
                    ipset_free(ips);
                    return NULL;
                }
                break;

            default:
                fprintf(stderr, "%s: Cannot understand result code. This is an internal error.\n", PROG);
                exit(1);
        }
    } while(likely(ips && fgets(line, MAX_LINE, fp)));

    if(likely(fp != stdin)) fclose(fp);

    if(unlikely(dns_done(ips))) {
        ipset_free(ips);
        return NULL;
    }

    if(unlikely(!ips)) return NULL;

    if(unlikely(parse_errors)) {
        ipset_free(ips);
        return NULL;
    }

    if(unlikely(debug)) fprintf(stderr, "%s: Loaded %s %s\n", PROG, (ips->flags & IPSET_FLAG_OPTIMIZED)?"optimized":"non-optimized", ips->filename);

    /*
     * if(unlikely(!ips->entries)) {
     *	free(ips);
     *	return NULL;
     * }
     */

    return ips;
}
