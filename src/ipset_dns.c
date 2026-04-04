#include "iprange.h"

/*
 * the maximum line element to read in input files
 * normally the elements are IP, IP/MASK, HOSTNAME
 */
#define MAX_INPUT_ELEMENT 255

/* ----------------------------------------------------------------------------
 * hostname resolution
 */

typedef struct dnsreq {
    struct dnsreq *next;
    char tries;
    char hostname[];
} DNSREQ;

typedef struct dnsrep {
    in_addr_t ip;
    struct dnsrep *next;
} DNSREP;

static DNSREQ *dns_requests;
static DNSREP *dns_replies;
static int dns_threads;
int dns_threads_max = 5;
int dns_silent;
int dns_progress;
static unsigned long dns_requests_pending;
static unsigned long dns_requests_made;
static unsigned long dns_requests_finished;
static unsigned long dns_requests_retries;
static unsigned long dns_replies_found;
static unsigned long dns_replies_failed;

static pthread_cond_t dns_cond = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t dns_requests_mut = PTHREAD_MUTEX_INITIALIZER;
static pthread_mutex_t dns_replies_mut = PTHREAD_MUTEX_INITIALIZER;

static void dns_lock_requests(void)   { pthread_mutex_lock(&dns_requests_mut); }
static void dns_unlock_requests(void) { pthread_mutex_unlock(&dns_requests_mut); }
static void dns_lock_replies(void)    { pthread_mutex_lock(&dns_replies_mut); }
static void dns_unlock_replies(void)  { pthread_mutex_unlock(&dns_replies_mut); }

void dns_reset_stats(void)
{
    dns_lock_requests();
    dns_requests = NULL;
    dns_requests_pending = 0;
    dns_requests_made = 0;
    dns_requests_finished = 0;
    dns_requests_retries = 0;
    dns_replies_found = 0;
    dns_replies_failed = 0;
    dns_unlock_requests();

    dns_lock_replies();
    dns_replies = NULL;
    dns_unlock_replies();
}

// the threads waiting for requests
static void dns_thread_wait_for_requests(void) {
    dns_lock_requests();
    while(!dns_requests)
        pthread_cond_wait(&dns_cond, &dns_requests_mut);
    dns_unlock_requests();
}

// the master signals the threads for new requests
static void dns_signal_threads(void)
{
    /* signal the childs we have a new request for them */
    dns_lock_requests();
    pthread_cond_signal(&dns_cond);
    dns_unlock_requests();
}


static void *dns_thread_resolve(void *ptr);

/* ----------------------------------------------------------------------------
 * dns_request_add()
 *
 * add a new DNS resolution request to the queue
 *
 */

static int dns_request_add(DNSREQ *d)
{
    unsigned long pending;

    dns_lock_requests();
    d->next = dns_requests;
    dns_requests = d;
    dns_requests_pending++;
    dns_requests_made++;

    pending = dns_requests_pending;
    dns_unlock_requests();

    /* do we have start a new thread? */
    if(pending > (unsigned long)dns_threads && dns_threads < dns_threads_max) {
        pthread_t thread;

        if(unlikely(debug))
            fprintf(stderr, "%s: Creating new DNS thread\n", PROG);

        if(pthread_create(&thread, NULL, dns_thread_resolve, NULL)) {
            fprintf(stderr, "%s: Cannot create DNS thread.\n", PROG);
            if(dns_threads == 0) {
                dns_lock_requests();
                dns_requests = d->next;
                dns_requests_pending--;
                dns_requests_made--;
                dns_unlock_requests();

                free(d);
                return -1;
            }
        }
        else {
            dns_threads++;
            if(pthread_detach(thread))
                fprintf(stderr, "%s: Cannot detach DNS thread.\n", PROG);
        }
    }

    dns_signal_threads();
    return 0;
}


/* ----------------------------------------------------------------------------
 * dns_request_done()
 *
 * to be called by a worker thread
 * let the main thread know a DNS resolution has been completed
 *
 */

static void dns_request_done(DNSREQ *d, int added)
{
    dns_lock_requests();
    dns_requests_pending--;
    dns_requests_finished++;

    if(!added) dns_replies_failed++;
    else dns_replies_found += added;

    dns_unlock_requests();

    free(d);
}


/* ----------------------------------------------------------------------------
 * dns_request_failed()
 *
 * to be called by a worker thread
 * handle a DNS failure (mainly for retries)
 *
 */

static void dns_request_failed(DNSREQ *d, int added, int gai_error)
{
    switch(gai_error) {
        case EAI_AGAIN: /* The name server returned a temporary failure indication.  Try again later. */
            if(d->tries > 0) {
                if(!dns_silent)
                    fprintf(stderr, "%s: DNS: '%s' will be retried: %s\n", PROG, d->hostname, gai_strerror(gai_error));

                d->tries--;

                dns_lock_requests();
                d->next = dns_requests;
                dns_requests = d;
                dns_requests_retries++;
                dns_replies_found += added;
                dns_unlock_requests();
                return;
            }
            dns_request_done(d, added);
            return;

        case EAI_SYSTEM:
            fprintf(stderr, "%s: DNS: '%s' system error: %s\n", PROG, d->hostname, strerror(errno));
            dns_request_done(d, added);
            return;

        case EAI_SOCKTYPE: /* The requested socket type is not supported. */
        case EAI_SERVICE: /* The requested service is not available for the requested socket type. */
        case EAI_MEMORY: /* Out of memory. */
        case EAI_BADFLAGS: /* hints.ai_flags contains invalid flags; or, hints.ai_flags included AI_CANONNAME and name was NULL. */
            fprintf(stderr, "%s: DNS: '%s' error: %s\n", PROG, d->hostname, gai_strerror(gai_error));
            dns_request_done(d, added);
            return;

        case EAI_NONAME: /* The node or service is not known */
        case EAI_FAIL:   /* The name server returned a permanent failure indication. */
        case EAI_FAMILY: /* The requested address family is not supported. */
        default:
            if(!dns_silent)
                fprintf(stderr, "%s: DNS: '%s' failed permanently: %s\n", PROG, d->hostname, gai_strerror(gai_error));
            dns_request_done(d, added);
            return;
    }
}


/* ----------------------------------------------------------------------------
 * dns_request_get()
 *
 * to be called by a worker thread
 * get a request from the requests queue
 *
 */

static DNSREQ *dns_request_get(void)
{
    DNSREQ *ret = NULL;

    /*
     * if(unlikely(debug))
     * fprintf(stderr, "%s: DNS THREAD waiting for DNS REQUEST\n", PROG);
     */

    while(!ret) {
        dns_lock_requests();
        if(dns_requests) {
            ret = dns_requests;
            dns_requests = dns_requests->next;
            ret->next = NULL;
        }
        dns_unlock_requests();
        if(ret) continue;

        dns_thread_wait_for_requests();
    }

    return ret;
}


/* ----------------------------------------------------------------------------
 * dns_thread_resolve()
 *
 * a pthread worker to get requests and generate replies
 *
 */

static void *dns_thread_resolve(void *ptr)
{
    DNSREQ *d;

    if(ptr) { ; }

    /*
     * if(unlikely(debug))
     *	fprintf(stderr, "%s: DNS THREAD created\n", PROG);
     */

    while((d = dns_request_get())) {
        int added = 0;

        /*
         * if(unlikely(debug))
         *	fprintf(stderr, "%s: DNS THREAD resolving DNS REQUEST for '%s'\n", PROG, d->hostname);
         */

        int r;
        struct addrinfo *result, *rp, hints;

        hints.ai_family = AF_INET;
        hints.ai_socktype = SOCK_DGRAM;
        hints.ai_flags = 0;
        hints.ai_protocol = 0;

        r = getaddrinfo(d->hostname, "80", &hints, &result);
        if(r != 0) {
            dns_request_failed(d, 0, r);
            continue;
        }

        for (rp = result; rp != NULL; rp = rp->ai_next) {
            char host[MAX_INPUT_ELEMENT + 1] = "";
            network_addr_t net;
            int err = 0;
            DNSREP *p;

            r = getnameinfo(rp->ai_addr, rp->ai_addrlen, host, sizeof(host), NULL, 0, NI_NUMERICHOST);
            if (r != 0) {
                fprintf(stderr, "%s: DNS: '%s' failed to get IP string: %s\n", PROG, d->hostname, gai_strerror(r));
                continue;
            }

            net = str2netaddr(host, &err);
            if(err) {
                fprintf(stderr, "%s: DNS: '%s' cannot parse the IP '%s': %s\n", PROG, d->hostname, host, gai_strerror(r));
                continue;
            }

            p = malloc(sizeof(DNSREP));
            if(!p) {
                fprintf(stderr, "%s: DNS: out of memory while resolving host '%s'\n", PROG, d->hostname);
                continue;
            }

            if(unlikely(debug)) {
                char buf[IP2STR_MAX_LEN + 1];
                fprintf(stderr, "%s: DNS: '%s' = %s\n", PROG, d->hostname, ip2str_r(buf, net.addr));
            }

            p->ip = net.addr;
            dns_lock_replies();
            p->next = dns_replies;
            dns_replies = p;
            added++;
            dns_unlock_replies();
        }

        freeaddrinfo(result);
        dns_request_done(d, added);
    }

    return NULL;
}

/* ----------------------------------------------------------------------------
 * dns_process_replies()
 *
 * dequeue the resolved hostnames by adding them to the ipset
 *
 */

static void dns_process_replies(ipset *ips)
{
    dns_lock_replies();

    if(!dns_replies) {
        dns_unlock_replies();
        return;
    }

    while(dns_replies) {
        DNSREP *p;

        /*
         * if(unlikely(debug))
         * char buf[IP2STR_MAX_LEN + 1];
         * fprintf(stderr, "%s: Got DNS REPLY '%s'\n", PROG, ip2str_r(buf, dns_replies->ip));
         */

        ipset_add_ip_range(ips, dns_replies->ip, dns_replies->ip);

        p = dns_replies->next;
        free(dns_replies);
        dns_replies = p;
    }
    dns_unlock_replies();
}


/* ----------------------------------------------------------------------------
 * dns_request()
 *
 * attempt to resolv a hostname
 * the result (one or more) will be appended to the ipset supplied
 *
 * this is asynchronous - it will just queue the request and spawn worker
 * threads to do the DNS resolution.
 *
 * the IPs will be added to the ipset, either at the next call to this
 * function, or by calling dns_done().
 *
 * So, to use it:
 * 1. call dns_request() to request dns resolutions (any number)
 * 2. call dns_done() when you finish requesting hostnames
 * 3. the resolved IPs are in the ipset you supplied
 *
 * All ipset manipulation is done at this thread, so if control is
 * outside the above 2 functions, you are free to do whatever you like
 * with the ipset.
 *
 * Important: you cannot use dns_request() and dns_done() with more
 * than 1 ipset at the same time. The resulting IPs will be multiplexed.
 * When you call dns_done() on one ipset, you can proceed with the next.
 *
 */

int dns_request(ipset *ips, char *hostname)
{
    DNSREQ *d;

    /* dequeue if possible */
    dns_process_replies(ips);

    /*
     * if(unlikely(debug))
     *	fprintf(stderr, "%s: Adding DNS REQUEST for '%s'\n", PROG, hostname);
     */

    d = malloc(sizeof(DNSREQ) + strlen(hostname) + 1);
    if(!d) goto cleanup;

    strcpy(d->hostname, hostname);
    d->tries = 20;

    /* add the request to the queue */
    if(dns_request_add(d))
        return -1;

    return 0;

    cleanup:
    fprintf(stderr, "%s: out of memory, while trying to resolv '%s'\n", PROG, hostname);
    return -1;
}


/* ----------------------------------------------------------------------------
 * dns_done()
 *
 * wait for the DNS requests made to finish.
 *
 */

int dns_done(ipset *ips)
{
    unsigned long dots = 40, shown = 0, should_show = 0;
    unsigned long pending, made, finished, retries, replies_found, replies_failed;

    if(ips) { ; }

    dns_lock_requests();
    made = dns_requests_made;
    dns_unlock_requests();

    if(!made) {
        dns_reset_stats();
        return 0;
    }

    while(1) {
        dns_lock_requests();
        pending = dns_requests_pending;
        made = dns_requests_made;
        finished = dns_requests_finished;
        retries = dns_requests_retries;
        replies_found = dns_replies_found;
        replies_failed = dns_replies_failed;
        dns_unlock_requests();

        if(!pending) break;

        if(unlikely(debug))
            fprintf(stderr, "%s: DNS: waiting %lu DNS resolutions to finish...\n", PROG, pending);
        else if(dns_progress) {
            should_show = dots * finished / made;
            for(; shown < should_show; shown++) {
                if(!(shown % 10)) fprintf(stderr, "%lu%%", shown * 100 / dots);
                else fprintf(stderr, ".");
            }
        }

        dns_process_replies(ips);

        if(pending) {
            dns_signal_threads();
            sleep(1);
        }
    }
    dns_process_replies(ips);

    dns_lock_requests();
    made = dns_requests_made;
    retries = dns_requests_retries;
    replies_found = dns_replies_found;
    replies_failed = dns_replies_failed;
    dns_unlock_requests();

    if(unlikely(debug))
        fprintf(stderr, "%s: DNS: made %lu DNS requests, failed %lu, retries: %lu, IPs got %lu, threads used %d of %d\n", PROG, made, replies_failed, retries, replies_found, dns_threads, dns_threads_max);
    else if(dns_progress) {
        for(; shown <= dots; shown++) {
            if(!(shown % 10)) fprintf(stderr, "%lu%%", shown * 100 / dots);
            else fprintf(stderr, ".");
        }
        fprintf(stderr, "\n");
    }

    dns_reset_stats();
    return (replies_failed != 0);
}
