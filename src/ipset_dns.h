#ifndef IPRANGE_IPSET_DNS_H
#define IPRANGE_IPSET_DNS_H

extern int dns_threads_max;
extern int dns_silent;
extern int dns_progress;

extern int dns_request(ipset *ips, char *hostname);
extern int dns_done(ipset *ips);
extern void dns_reset_stats(void);

#endif //IPRANGE_IPSET_DNS_H
