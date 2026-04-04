#ifndef IPRANGE_IPSET6_DNS_H
#define IPRANGE_IPSET6_DNS_H

extern int dns6_request(ipset6 *ips, char *hostname);
extern int dns6_done(ipset6 *ips);
extern void dns6_reset_stats(void);

#endif //IPRANGE_IPSET6_DNS_H
