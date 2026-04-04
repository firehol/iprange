#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"
#include "ipset6_binary.h"
#include "ipset6_dns.h"
#include "ipset6_load.h"

#define MAX_INPUT_ELEMENT6 256

/* address family for the current invocation */
extern int active_family;
extern unsigned long ipv6_dropped_in_ipv4_mode;

/*
 * Classify a token as IPv6, IPv4, or hostname.
 * Returns:
 *   6 = definitely IPv6 (contains ':')
 *   4 = looks like IPv4 (digits/dots/slash, no colons)
 *   0 = hostname or unknown
 */
static inline int classify_address(const char *token) {
    if(strchr(token, ':')) return 6;
    if(strchr(token, '.') || strchr(token, '/')) return 4;
    /* pure digits could be IPv4 integer or hostname */
    const char *s = token;
    int all_digits = 1;
    while(*s) {
        if(*s < '0' || *s > '9') { all_digits = 0; break; }
        s++;
    }
    if(all_digits && s != token) return 4;
    return 0;
}

/*
 * Parse a line that may contain IPv6 addresses.
 * Returns the same IPSET_LINE_TYPE enum values as the IPv4 parser.
 */
typedef enum {
    LINE6_IS_INVALID = -1,
    LINE6_IS_EMPTY = 0,
    LINE6_HAS_1_IP = 1,
    LINE6_HAS_2_IPS = 2,
    LINE6_HAS_1_HOSTNAME = 3
} IPSET6_LINE_TYPE;

static inline int is_ipv6_char(char c) {
    return ((c >= '0' && c <= '9')
         || (c >= 'a' && c <= 'f')
         || (c >= 'A' && c <= 'F')
         || c == ':' || c == '.' || c == '/');
}

static inline int is_hostname_char6(char c) {
    return ((c >= '0' && c <= '9')
         || (c >= 'a' && c <= 'z')
         || (c >= 'A' && c <= 'Z')
         || c == '_' || c == '-' || c == '.');
}

static inline IPSET6_LINE_TYPE parse_line6(char *line, int lineid, char *ipstr, char *ipstr2, int len) {
    char *s = line;
    int i = 0;
    int has_colon = 0;

    (void)lineid;

    while(*s == ' ' || *s == '\t') s++;
    if(*s == '#' || *s == ';') return LINE6_IS_EMPTY;
    if(*s == '\r' || *s == '\n' || *s == '\0') return LINE6_IS_EMPTY;

    /* scan first token: accept IPv6 chars (hex digits, colons, dots, slash) */
    while(i < len && is_ipv6_char(*s)) {
        if(*s == ':') has_colon = 1;
        ipstr[i++] = *s++;
    }

    /* if no chars matched in the IPv6 set, try hostname */
    if(!i) {
        i = 0;
        s = line;
        while(*s == ' ' || *s == '\t') s++;
        while(i < len && is_hostname_char6(*s))
            ipstr[i++] = *s++;
        if(!i) return LINE6_IS_INVALID;
        ipstr[i] = '\0';
        while(*s == ' ' || *s == '\t') s++;
        if(*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0')
            return LINE6_HAS_1_HOSTNAME;
        return LINE6_IS_INVALID;
    }

    ipstr[i] = '\0';

    while(*s == ' ' || *s == '\t') s++;
    if(*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0')
        return LINE6_HAS_1_IP;

    if(*s != '-') {
        /* if first token has no colon and doesn't look like an IP, try hostname */
        if(!has_colon && classify_address(ipstr) == 0) {
            i = 0;
            s = line;
            while(*s == ' ' || *s == '\t') s++;
            while(i < len && is_hostname_char6(*s))
                ipstr[i++] = *s++;
            if(i) {
                ipstr[i] = '\0';
                while(*s == ' ' || *s == '\t') s++;
                if(*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0')
                    return LINE6_HAS_1_HOSTNAME;
            }
        }
        return LINE6_IS_INVALID;
    }

    /* skip the dash */
    s++;
    while(*s == ' ' || *s == '\t') s++;

    if(*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0') {
        fprintf(stderr, "%s: Incomplete range on line, expected an address after -\n", PROG);
        return LINE6_HAS_1_IP;
    }

    /* scan second token */
    i = 0;
    while(i < len && is_ipv6_char(*s))
        ipstr2[i++] = *s++;

    if(!i) return LINE6_IS_INVALID;
    ipstr2[i] = '\0';

    while(*s == ' ' || *s == '\t') s++;
    if(*s == '#' || *s == ';' || *s == '\r' || *s == '\n' || *s == '\0')
        return LINE6_HAS_2_IPS;

    return LINE6_IS_INVALID;
}

/*
 * Parse an address string in IPv6 mode.
 * Accepts both IPv6 and IPv4 (normalizing IPv4 to mapped IPv6).
 */
static network_addr6_t parse_address6(char *ipstr, int *err) {
    network_addr6_t netaddr;
    int addr_class = classify_address(ipstr);

    if(addr_class == 6) {
        return str2netaddr6(ipstr, err);
    }
    else if(addr_class == 4) {
        network_addr_t v4 = str2netaddr(ipstr, err);
        if(*err) {
            netaddr.addr = 0;
            netaddr.broadcast = 0;
            return netaddr;
        }

        netaddr.addr = ipv4_to_mapped6(v4.addr);
        netaddr.broadcast = ipv4_to_mapped6(v4.broadcast);
        return netaddr;
    }

    if(err) (*err)++;
    fprintf(stderr, "%s: Cannot parse address: %s\n", PROG, ipstr);
    netaddr.addr = 0;
    netaddr.broadcast = 0;
    return netaddr;
}


/* ----------------------------------------------------------------------------
 * ipset6_load()
 *
 * Load a file into an IPv6 ipset.
 * - IPv6 addresses are parsed directly
 * - IPv4 addresses are normalized to IPv4-mapped IPv6
 * - Hostnames are resolved for both AAAA and A records
 */
ipset6 *ipset6_load(const char *filename) {
    FILE *fp = stdin;
    int lineid = 0;
    int parse_errors = 0;
    char line[MAX_LINE + 1], ipstr[MAX_INPUT_ELEMENT6 + 1], ipstr2[MAX_INPUT_ELEMENT6 + 1];
    ipset6 *ips = ipset6_create((filename && *filename)?filename:"stdin", 0);

    if(unlikely(!ips)) return NULL;

    if(likely(filename && *filename)) {
        fp = fopen(filename, "r");
        if(unlikely(!fp)) {
            fprintf(stderr, "%s: %s - %s\n", PROG, filename, strerror(errno));
            ipset6_free(ips);
            return NULL;
        }
    }

    if(unlikely(debug)) fprintf(stderr, "%s: Loading from %s (IPv6 mode)\n", PROG, ips->filename);

    ips->flags |= IPSET_FLAG_OPTIMIZED;

    if(!fgets(line, MAX_LINE, fp)) {
        if(likely(fp != stdin)) fclose(fp);
        return ips;
    }

    /* strip UTF-8 BOM if present on first line */
    if((unsigned char)line[0] == 0xEF && (unsigned char)line[1] == 0xBB && (unsigned char)line[2] == 0xBF)
        memmove(line, line + 3, strlen(line + 3) + 1);

    /* check for binary headers */
    if(!strcmp(line, BINARY_HEADER_V20)) {
        if(ipset6_load_binary_v20(fp, ips, 1)) {
            fprintf(stderr, "%s: Cannot load binary v2 %s\n", PROG, filename);
            ipset6_free(ips);
            ips = NULL;
        }
        if(likely(fp != stdin)) fclose(fp);
        return ips;
    }

    /* reject v1.0 binary in IPv6 mode */
    if(!strcmp(line, BINARY_HEADER_V10)) {
        fprintf(stderr, "%s: %s: IPv4 binary file cannot be loaded in IPv6 mode\n", PROG, ips->filename);
        ipset6_free(ips);
        if(likely(fp != stdin)) fclose(fp);
        return NULL;
    }

    do {
        lineid++;

        switch(parse_line6(line, lineid, ipstr, ipstr2, MAX_INPUT_ELEMENT6)) {
            case LINE6_IS_INVALID:
                fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                parse_errors = 1;
                break;

            case LINE6_IS_EMPTY:
                break;

            case LINE6_HAS_1_IP:
            {
                int err = 0;
                network_addr6_t net = parse_address6(ipstr, &err);
                if(unlikely(err)) {
                    fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                    parse_errors = 1;
                }
                else {
                    ipset6_add_ip_range(ips, net.addr, net.broadcast);
                }
            }
                break;

            case LINE6_HAS_2_IPS:
            {
                int err = 0;
                network_addr6_t net1 = parse_address6(ipstr, &err);
                network_addr6_t net2;
                if(likely(!err)) net2 = parse_address6(ipstr2, &err);
                if(unlikely(err)) {
                    fprintf(stderr, "%s: Cannot understand line No %d from %s: %s\n", PROG, lineid, ips->filename, line);
                    parse_errors = 1;
                    continue;
                }

                /* check for mixed-family range endpoints */
                int c1 = classify_address(ipstr);
                int c2 = classify_address(ipstr2);
                if(c1 != c2 && c1 != 0 && c2 != 0) {
                    fprintf(stderr, "%s: Mixed-family range on line %d: %s - %s\n", PROG, lineid, ipstr, ipstr2);
                    parse_errors = 1;
                    continue;
                }

                ipv6_addr_t lo = (net1.addr < net2.addr) ? net1.addr : net2.addr;
                ipv6_addr_t hi = (net1.broadcast > net2.broadcast) ? net1.broadcast : net2.broadcast;
                ipset6_add_ip_range(ips, lo, hi);
            }
                break;

            case LINE6_HAS_1_HOSTNAME:
                if(unlikely(debug))
                    fprintf(stderr, "%s: DNS resolution for hostname '%s' from line %d of file %s (IPv6 mode).\n", PROG, ipstr, lineid, ips->filename);

                if(unlikely(dns6_request(ips, ipstr))) {
                    if(likely(fp != stdin)) fclose(fp);
                    dns6_reset_stats();
                    ipset6_free(ips);
                    return NULL;
                }
                break;

            default:
                fprintf(stderr, "%s: Cannot understand result code. This is an internal error.\n", PROG);
                exit(1);
        }
    } while(likely(ips && fgets(line, MAX_LINE, fp)));

    if(likely(fp != stdin)) fclose(fp);

    if(unlikely(dns6_done(ips))) {
        ipset6_free(ips);
        return NULL;
    }

    if(unlikely(!ips)) return NULL;

    if(unlikely(parse_errors)) {
        ipset6_free(ips);
        return NULL;
    }

    return ips;
}
