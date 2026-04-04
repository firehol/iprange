#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

/* ----------------------------------------------------------------------------
 * hostname resolution — IPv6 DNS thread pool
 *
 * resolves both AAAA and A records;
 * A records are normalized to IPv4-mapped IPv6 (::ffff:x.x.x.x)
 */

extern int dns_threads_max;
extern int dns_silent;

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

void dns6_reset_stats(void)
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

int dns6_request(ipset6 *ips, char *hostname)
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

int dns6_done(ipset6 *ips)
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
