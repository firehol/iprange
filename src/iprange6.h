#ifndef IPRANGE_IPRANGE6_H
#define IPRANGE_IPRANGE6_H

#include "iprange.h"
#include <string.h>

/* IPv6 address type: 128-bit unsigned integer in host byte order */
typedef __uint128_t ipv6_addr_t;

/* IPv6 network address type: one field for the net address, one for broadcast */
typedef struct network_addr6 {
    ipv6_addr_t addr;
    ipv6_addr_t broadcast;
} network_addr6_t;

/* Maximum IPv6 address */
#define IPV6_ADDR_MAX ((ipv6_addr_t)((__uint128_t)(-1)))

/* IPv4-mapped IPv6 prefix: ::ffff:0:0/96 */
#define IPV6_MAPPED_PREFIX ((ipv6_addr_t)0xFFFF00000000ULL)
#define IPV6_MAPPED_MASK   ((ipv6_addr_t)0xFFFFFFFFULL)

#define IP6STR_MAX_LEN 46

/*----------------------------------------------------------------------*/
/* Convert between struct in6_addr (network byte order) and ipv6_addr_t */
/* (host byte order, big-endian logical order: MSB first)               */
/*----------------------------------------------------------------------*/

static inline ipv6_addr_t in6_addr_to_ipv6(const struct in6_addr *in6) {
    ipv6_addr_t result = 0;
    int i;
    for(i = 0; i < 16; i++)
        result = (result << 8) | in6->s6_addr[i];
    return result;
}

static inline void ipv6_to_in6_addr(ipv6_addr_t addr, struct in6_addr *in6) {
    int i;
    for(i = 15; i >= 0; i--) {
        in6->s6_addr[i] = (uint8_t)(addr & 0xFF);
        addr >>= 8;
    }
}

/*----------------------------------------------*/
/* Compute netmask for IPv6 given prefix length */
/*----------------------------------------------*/
static inline ipv6_addr_t netmask6(int prefix) {
    if(prefix == 0)
        return (ipv6_addr_t)0;
    if(prefix >= 128)
        return IPV6_ADDR_MAX;
    return IPV6_ADDR_MAX << (128 - prefix);
}

/*----------------------------------------------------*/
/* Compute broadcast address given address and prefix  */
/*----------------------------------------------------*/
static inline ipv6_addr_t broadcast6(ipv6_addr_t addr, int prefix) {
    return addr | ~netmask6(prefix);
}

/*--------------------------------------------------*/
/* Compute network address given address and prefix  */
/*--------------------------------------------------*/
static inline ipv6_addr_t network6(ipv6_addr_t addr, int prefix) {
    return addr & netmask6(prefix);
}

/*------------------------------------------------------------------*/
/* Set a bit to a given value (0 or 1); MSB is bit 1, LSB is bit 128 */
/*------------------------------------------------------------------*/
static inline ipv6_addr_t set_bit6(ipv6_addr_t addr, int bitno, int val) {
    if(val)
        return addr | ((__uint128_t)1 << (128 - bitno));
    else
        return addr & ~((__uint128_t)1 << (128 - bitno));
}

/*-----------------------------------------------------------*/
/* Format an IPv6 address to string using inet_ntop           */
/*-----------------------------------------------------------*/
static inline char *ip6str_r(char *buf, ipv6_addr_t addr) {
    struct in6_addr in6;
    ipv6_to_in6_addr(addr, &in6);
    inet_ntop(AF_INET6, &in6, buf, IP6STR_MAX_LEN);
    return buf;
}

/*-----------------------------------------------------------*/
/* Parse an IPv6 address string using inet_pton               */
/* Returns 1 on success, 0 on failure                         */
/*-----------------------------------------------------------*/
static inline int str_to_ipv6(const char *str, ipv6_addr_t *addr) {
    struct in6_addr in6;
    if(inet_pton(AF_INET6, str, &in6) != 1)
        return 0;
    *addr = in6_addr_to_ipv6(&in6);
    return 1;
}

/*-----------------------------------------------------------*/
/* Parse an IPv6 address/prefix string                        */
/* Handles: full notation, compressed, dotted-tail mapped     */
/*-----------------------------------------------------------*/
static inline network_addr6_t str2netaddr6(char *ipstr, int *err) {
    int prefix = 128;
    char *prefixstr;
    network_addr6_t netaddr;
    ipv6_addr_t addr;

    if((prefixstr = strchr(ipstr, '/'))) {
        char *endptr = NULL;
        long parsed_prefix;
        *prefixstr = '\0';
        prefixstr++;
        errno = 0;
        parsed_prefix = strtol(prefixstr, &endptr, 10);
        if(unlikely(errno || !endptr || endptr == prefixstr || *endptr != '\0'
                    || parsed_prefix < 0 || parsed_prefix > 128)) {
            if(err) (*err)++;
            fprintf(stderr, "%s: Invalid IPv6 prefix /%s\n", PROG, prefixstr);
            netaddr.addr = 0;
            netaddr.broadcast = 0;
            return netaddr;
        }
        prefix = (int)parsed_prefix;
    }

    if(!str_to_ipv6(ipstr, &addr)) {
        if(err) (*err)++;
        fprintf(stderr, "%s: Invalid IPv6 address %s\n", PROG, ipstr);
        netaddr.addr = 0;
        netaddr.broadcast = 0;
        return netaddr;
    }

    if(likely(cidr_use_network))
        netaddr.addr = network6(addr, prefix);
    else
        netaddr.addr = addr;

    netaddr.broadcast = broadcast6(netaddr.addr, prefix);
    return netaddr;
}

/*-----------------------------------------------------------*/
/* Check if an IPv6 address is IPv4-mapped (::ffff:x.x.x.x)  */
/*-----------------------------------------------------------*/
static inline int is_ipv4_mapped(ipv6_addr_t addr) {
    return (addr >> 32) == 0xFFFF;
}

/*-----------------------------------------------------------*/
/* Convert IPv4 to IPv4-mapped IPv6                           */
/*-----------------------------------------------------------*/
static inline ipv6_addr_t ipv4_to_mapped6(in_addr_t ipv4) {
    return IPV6_MAPPED_PREFIX | (ipv6_addr_t)ipv4;
}

/*-----------------------------------------------------------*/
/* Extract IPv4 from IPv4-mapped IPv6                         */
/*-----------------------------------------------------------*/
static inline in_addr_t mapped6_to_ipv4(ipv6_addr_t addr) {
    return (in_addr_t)(addr & IPV6_MAPPED_MASK);
}

/*-----------------------------------------------------------*/
/* Format a 128-bit unsigned integer to decimal string         */
/* Returns pointer to start of number within buf               */
/* buf must be at least 40 bytes                               */
/*-----------------------------------------------------------*/
static inline char *u128_to_dec(char *buf, size_t buflen, __uint128_t val) {
    char *p = buf + buflen - 1;
    *p = '\0';

    if(val == 0) {
        *(--p) = '0';
        return p;
    }

    while(val > 0) {
        *(--p) = '0' + (char)(val % 10);
        val /= 10;
    }
    return p;
}

#endif /* IPRANGE_IPRANGE6_H */
