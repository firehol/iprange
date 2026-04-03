#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"
#include "ipset6_binary.h"
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
 *
 * Accepted formats:
 *   - IPv6 address: 2001:db8::1
 *   - IPv6 CIDR: 2001:db8::/32
 *   - IPv6 range: 2001:db8::1 - 2001:db8::ff
 *   - IPv4 address (for normalization to mapped IPv6)
 *   - hostname (for DNS resolution)
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
        /* try as hostname */
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
 * Returns the parsed network_addr6_t.
 */
static network_addr6_t parse_address6(char *ipstr, int *err) {
    network_addr6_t netaddr;
    int addr_class = classify_address(ipstr);

    if(addr_class == 6) {
        /* IPv6 literal */
        return str2netaddr6(ipstr, err);
    }
    else if(addr_class == 4) {
        /* IPv4 literal: normalize to mapped IPv6 */
        network_addr_t v4 = str2netaddr(ipstr, err);
        if(*err) {
            netaddr.addr = 0;
            netaddr.broadcast = 0;
            return netaddr;
        }

        /* handle CIDR: if the IPv4 had a prefix, map the range */
        netaddr.addr = ipv4_to_mapped6(v4.addr);
        netaddr.broadcast = ipv4_to_mapped6(v4.broadcast);
        return netaddr;
    }

    /* unknown format */
    if(err) (*err)++;
    fprintf(stderr, "%s: Cannot parse address: %s\n", PROG, ipstr);
    netaddr.addr = 0;
    netaddr.broadcast = 0;
    return netaddr;
}

/* DNS structures and functions from ipset_load.c */
extern int dns_threads_max;
extern int dns_silent;
extern int dns_progress;

/* IPv6 DNS resolution types */
typedef struct dnsreq6 {
    struct dnsreq6 *next;
    char tries;
    char hostname[];
} DNSREQ6;

typedef struct dnsrep6 {
    ipv6_addr_t ip;
    struct dnsrep6 *next;
} DNSREP6;

static DNSREQ6 *dns6_requests;
static DNSREP6 *dns6_replies;
static int dns6_threads;
static unsigned long dns6_requests_pending;
static unsigned long dns6_requests_made;
static unsigned long dns6_requests_finished;
static unsigned long dns6_requests_retries;
static unsigned long dns6_replies_found;
static unsigned long dns6_replies_failed;

static pthread_cond_t dns6_cond = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t dns6_requests_mut = PTHREAD_MUTEX_INITIALIZER;
static pthread_mutex_t dns6_replies_mut = PTHREAD_MUTEX_INITIALIZER;

static void dns6_reset_stats(void)
{
    pthread_mutex_lock(&dns6_requests_mut);
    dns6_requests = NULL;
    dns6_requests_pending = 0;
    dns6_requests_made = 0;
    dns6_requests_finished = 0;
    dns6_requests_retries = 0;
    dns6_replies_found = 0;
    dns6_replies_failed = 0;
    pthread_mutex_unlock(&dns6_requests_mut);

    pthread_mutex_lock(&dns6_replies_mut);
    dns6_replies = NULL;
    pthread_mutex_unlock(&dns6_replies_mut);
}

static void *dns6_thread_resolve(void *ptr);

static void dns6_signal_threads(void)
{
    pthread_mutex_lock(&dns6_requests_mut);
    pthread_cond_signal(&dns6_cond);
    pthread_mutex_unlock(&dns6_requests_mut);
}

static int dns6_request_add(DNSREQ6 *d)
{
    unsigned long pending;

    pthread_mutex_lock(&dns6_requests_mut);
    d->next = dns6_requests;
    dns6_requests = d;
    dns6_requests_pending++;
    dns6_requests_made++;
    pending = dns6_requests_pending;
    pthread_mutex_unlock(&dns6_requests_mut);

    if(pending > (unsigned long)dns6_threads && dns6_threads < dns_threads_max) {
        pthread_t thread;
        if(pthread_create(&thread, NULL, dns6_thread_resolve, NULL)) {
            fprintf(stderr, "%s: Cannot create DNS thread.\n", PROG);
            if(dns6_threads == 0) {
                pthread_mutex_lock(&dns6_requests_mut);
                dns6_requests = d->next;
                dns6_requests_pending--;
                dns6_requests_made--;
                pthread_mutex_unlock(&dns6_requests_mut);
                free(d);
                return -1;
            }
        }
        else {
            dns6_threads++;
            pthread_detach(thread);
        }
    }

    dns6_signal_threads();
    return 0;
}

static void dns6_request_done(DNSREQ6 *d, int added)
{
    pthread_mutex_lock(&dns6_requests_mut);
    dns6_requests_pending--;
    dns6_requests_finished++;
    if(!added) dns6_replies_failed++;
    else dns6_replies_found += added;
    pthread_mutex_unlock(&dns6_requests_mut);
    free(d);
}

static void dns6_request_failed(DNSREQ6 *d, int added, int gai_error)
{
    switch(gai_error) {
        case EAI_AGAIN:
            if(d->tries > 0) {
                if(!dns_silent)
                    fprintf(stderr, "%s: DNS: '%s' will be retried: %s\n", PROG, d->hostname, gai_strerror(gai_error));
                d->tries--;
                pthread_mutex_lock(&dns6_requests_mut);
                d->next = dns6_requests;
                dns6_requests = d;
                dns6_requests_retries++;
                dns6_replies_found += added;
                pthread_mutex_unlock(&dns6_requests_mut);
                return;
            }
            /* fall through */
        default:
            if(!dns_silent)
                fprintf(stderr, "%s: DNS: '%s' failed: %s\n", PROG, d->hostname, gai_strerror(gai_error));
            dns6_request_done(d, added);
            return;
    }
}

static DNSREQ6 *dns6_request_get(void)
{
    DNSREQ6 *ret = NULL;

    while(!ret) {
        pthread_mutex_lock(&dns6_requests_mut);
        if(dns6_requests) {
            ret = dns6_requests;
            dns6_requests = dns6_requests->next;
            ret->next = NULL;
        }
        pthread_mutex_unlock(&dns6_requests_mut);
        if(ret) continue;

        pthread_mutex_lock(&dns6_requests_mut);
        while(!dns6_requests)
            pthread_cond_wait(&dns6_cond, &dns6_requests_mut);
        pthread_mutex_unlock(&dns6_requests_mut);
    }

    return ret;
}

/*
 * DNS thread for IPv6 mode: resolves both AAAA and A records.
 * A records are normalized to IPv4-mapped IPv6 (::ffff:x.x.x.x).
 */
static void *dns6_thread_resolve(void *ptr)
{
    DNSREQ6 *d;
    (void)ptr;

    while((d = dns6_request_get())) {
        int added = 0;
        int r;
        struct addrinfo *result, *rp, hints;

        /* resolve both IPv4 and IPv6 */
        memset(&hints, 0, sizeof(hints));
        hints.ai_family = AF_UNSPEC;
        hints.ai_socktype = SOCK_DGRAM;

        r = getaddrinfo(d->hostname, "80", &hints, &result);
        if(r != 0) {
            dns6_request_failed(d, 0, r);
            continue;
        }

        for(rp = result; rp != NULL; rp = rp->ai_next) {
            DNSREP6 *p;
            ipv6_addr_t ip;

            if(rp->ai_family == AF_INET6) {
                struct sockaddr_in6 *sa6 = (struct sockaddr_in6 *)rp->ai_addr;
                ip = in6_addr_to_ipv6(&sa6->sin6_addr);
            }
            else if(rp->ai_family == AF_INET) {
                struct sockaddr_in *sa4 = (struct sockaddr_in *)rp->ai_addr;
                ip = ipv4_to_mapped6(ntohl(sa4->sin_addr.s_addr));
            }
            else continue;

            p = malloc(sizeof(DNSREP6));
            if(!p) {
                fprintf(stderr, "%s: DNS: out of memory while resolving host '%s'\n", PROG, d->hostname);
                continue;
            }

            p->ip = ip;
            pthread_mutex_lock(&dns6_replies_mut);
            p->next = dns6_replies;
            dns6_replies = p;
            added++;
            pthread_mutex_unlock(&dns6_replies_mut);
        }

        freeaddrinfo(result);
        dns6_request_done(d, added);
    }

    return NULL;
}

static void dns6_process_replies(ipset6 *ips)
{
    pthread_mutex_lock(&dns6_replies_mut);
    while(dns6_replies) {
        DNSREP6 *p;
        ipset6_add_ip_range(ips, dns6_replies->ip, dns6_replies->ip);
        p = dns6_replies->next;
        free(dns6_replies);
        dns6_replies = p;
    }
    pthread_mutex_unlock(&dns6_replies_mut);
}

static int dns6_request(ipset6 *ips, char *hostname)
{
    DNSREQ6 *d;

    dns6_process_replies(ips);

    d = malloc(sizeof(DNSREQ6) + strlen(hostname) + 1);
    if(!d) {
        fprintf(stderr, "%s: out of memory, while trying to resolve '%s'\n", PROG, hostname);
        return -1;
    }

    strcpy(d->hostname, hostname);
    d->tries = 20;

    if(dns6_request_add(d))
        return -1;

    return 0;
}

static int dns6_done(ipset6 *ips)
{
    unsigned long pending, made;

    pthread_mutex_lock(&dns6_requests_mut);
    made = dns6_requests_made;
    pthread_mutex_unlock(&dns6_requests_mut);

    if(!made) {
        dns6_reset_stats();
        return 0;
    }

    while(1) {
        pthread_mutex_lock(&dns6_requests_mut);
        pending = dns6_requests_pending;
        pthread_mutex_unlock(&dns6_requests_mut);

        if(!pending) break;

        dns6_process_replies(ips);

        if(pending) {
            dns6_signal_threads();
            sleep(1);
        }
    }
    dns6_process_replies(ips);

    dns6_reset_stats();
    return 0;
}

/*
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
