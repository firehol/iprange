#ifndef IPRANGE_IPSET_LOAD_H
#define IPRANGE_IPSET_LOAD_H

extern int dns_threads_max;
extern int dns_silent;
extern int dns_progress;

extern ipset *ipset_load(const char *filename);

#endif //IPRANGE_IPSET_LOAD_H
