#ifndef IPRANGE_IPRANGE_H
#define IPRANGE_IPRANGE_H

#ifdef HAVE_CONFIG_H
#include <config.h>
#endif
#if defined(HAVE_INTTYPES_H)
#include <inttypes.h>
#elif defined(HAVE_STDINT_H)
#include <stdint.h>
#endif
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/time.h>
#include <sys/types.h>
#include <netdb.h>
#include <pthread.h>
#include <unistd.h>

extern int cidr_use_network;
extern int default_prefix;
extern char *PROG;
extern int debug;

/*---------------------------------------------------------------------*/
/* network address type: one field for the net address, one for prefix */
/*---------------------------------------------------------------------*/
typedef struct network_addr {
    in_addr_t addr;
    in_addr_t broadcast;
} network_addr_t;

/*------------------------------------------------------------------*/
/* Set a bit to a given value (0 or 1); MSB is bit 1, LSB is bit 32 */
/*------------------------------------------------------------------*/
static inline in_addr_t set_bit(in_addr_t addr, int bitno, int val) {

    if (val)
        return (addr | (1 << (32 - bitno)));
    else
        return (addr & ~(1 << (32 - bitno)));

}				/* set_bit() */

/*--------------------------------------*/
/* Compute netmask address given prefix */
/*--------------------------------------*/
static inline in_addr_t netmask(int prefix) {

    if (prefix == 0)
        return (~((in_addr_t) - 1));
    else
        return (in_addr_t)(~((1 << (32 - prefix)) - 1));

}				/* netmask() */

/*----------------------------------------------------*/
/* Compute broadcast address given address and prefix */
/*----------------------------------------------------*/
static inline in_addr_t broadcast(in_addr_t addr, int prefix) {

    return (addr | ~netmask(prefix));

}				/* broadcast() */

/*--------------------------------------------------*/
/* Compute network address given address and prefix */
/*--------------------------------------------------*/
static inline in_addr_t network(in_addr_t addr, int prefix) {

    return (addr & netmask(prefix));

}				/* network() */

/*-----------------------------------------------------------*/
/* Convert an A.B.C.D address into a 32-bit host-order value */
/*-----------------------------------------------------------*/
static inline in_addr_t a_to_hl(char *ipstr, int *err) {
    struct in_addr in;

    if (unlikely(!inet_aton(ipstr, &in))) {
        fprintf(stderr, "%s: Invalid address %s.\n", PROG, ipstr);
        in.s_addr = 0;
        if(err) (*err)++;
        return (ntohl(in.s_addr));
    }

    return (ntohl(in.s_addr));

}				/* a_to_hl() */

/*-----------------------------------------------------------------*/
/* convert a network address char string into a host-order network */
/* address and an integer prefix value                             */
/*-----------------------------------------------------------------*/
static inline network_addr_t str2netaddr(char *ipstr, int *err) {

    int prefix = default_prefix;
    char *prefixstr;
    network_addr_t netaddr;

    if ((prefixstr = strchr(ipstr, '/'))) {
        *prefixstr = '\0';
        prefixstr++;
        errno = 0;
        prefix = atoi(prefixstr);
        if (unlikely(errno || (*prefixstr == '\0') || (prefix < 0) || (prefix > 32))) {
            /* try the netmask format */
            in_addr_t mask = ~a_to_hl(prefixstr, err);
            /*fprintf(stderr, "mask is %u (0x%08x)\n", mask, mask);*/
            prefix = 32;
            while((likely(mask & 0x00000001))) {
                mask >>= 1;
                prefix--;
            }

            if(unlikely(mask)) {
                if(err) (*err)++;
                fprintf(stderr, "%s: Invalid netmask %s\n", PROG, prefixstr);
                netaddr.addr = 0;
                netaddr.broadcast = 0;
                return (netaddr);
            }
        }
    }

    if(likely(cidr_use_network))
        netaddr.addr = network(a_to_hl(ipstr, err), prefix);
    else
        netaddr.addr = a_to_hl(ipstr, err);

    netaddr.broadcast = broadcast(netaddr.addr, prefix);

    return (netaddr);

}

// ----------------------------------------------------------------------------
// Print out a 32-bit address in A.B.C.D/M format
//
// very fast implementation of IP address printing
// this is 30% faster than the system default (inet_ntoa() based)
// http://stackoverflow.com/questions/1680365/integer-to-ip-address-c

static inline char *ip2str_r(char *buf, in_addr_t IP) {
    int i, k;
    for(i = 0, k = 0; i < 4; i++) {
        char c0 = (char)(((((IP & (0xff << ((3 - i) * 8))) >> ((3 - i) * 8))) / 100) + 0x30);
        if(c0 != '0') *(buf + k++) = c0;

        char c1 = (char)((((((IP & (0xff << ((3 - i) * 8))) >> ((3 - i) * 8))) % 100) / 10) + 0x30);
        if(!(c1 == '0' && c0 == '0')) *(buf + k++) = c1;

        *(buf + k) = (char)((((((IP & (0xff << ((3 - i) * 8)))) >> ((3 - i) * 8))) % 10) + 0x30);
        k++;

        if(i < 3) *(buf + k++) = '.';
    }
    *(buf + k) = 0;

    return buf;
}

#define IP2STR_MAX_LEN 20

#include "ipset.h"
#include "ipset_binary.h"
#include "ipset_combine.h"
#include "ipset_common.h"
#include "ipset_copy.h"
#include "ipset_diff.h"
#include "ipset_exclude.h"
#include "ipset_load.h"
#include "ipset_merge.h"
#include "ipset_optimize.h"
#include "ipset_print.h"
#include "ipset_reduce.h"

#endif //IPRANGE_IPRANGE_H
