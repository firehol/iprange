#ifndef IPRANGE_UINT128_H
#define IPRANGE_UINT128_H

#include <stdint.h>

/* Define IPRANGE_FORCE_PORTABLE_U128 to compile the portable (struct-based)
 * 128-bit path even on platforms that have native __uint128_t. This lets the
 * 32-bit code path be exercised by the test suite on a 64-bit host. */
#if defined(__SIZEOF_INT128__) && !defined(IPRANGE_FORCE_PORTABLE_U128)

/* ================================================================
 * Native 128-bit path (64-bit platforms with compiler support).
 * Every function compiles to the native operator — zero overhead.
 * ================================================================ */

typedef __uint128_t uint128_t;

#define U128_ZERO ((uint128_t)0)
#define U128_ONE  ((uint128_t)1)
#define U128_MAX  ((uint128_t)((__uint128_t)(-1)))

static inline uint128_t u128_from_u64(uint64_t v)  { return (uint128_t)v; }
static inline uint128_t u128_from_u32(uint32_t v)  { return (uint128_t)v; }

static inline uint64_t u128_hi64(uint128_t a) { return (uint64_t)(a >> 64); }
static inline uint64_t u128_lo64(uint128_t a) { return (uint64_t)a; }

static inline int u128_is_zero(uint128_t a)         { return a == 0; }
static inline int u128_eq(uint128_t a, uint128_t b)  { return a == b; }
static inline int u128_lt(uint128_t a, uint128_t b)  { return a < b; }
static inline int u128_gt(uint128_t a, uint128_t b)  { return a > b; }
static inline int u128_le(uint128_t a, uint128_t b)  { return a <= b; }
static inline int u128_ge(uint128_t a, uint128_t b)  { return a >= b; }

static inline uint128_t u128_add(uint128_t a, uint128_t b) { return a + b; }
static inline uint128_t u128_sub(uint128_t a, uint128_t b) { return a - b; }
static inline uint128_t u128_inc(uint128_t a) { return a + 1; }
static inline uint128_t u128_dec(uint128_t a) { return a - 1; }

static inline uint128_t u128_and(uint128_t a, uint128_t b) { return a & b; }
static inline uint128_t u128_or(uint128_t a, uint128_t b)  { return a | b; }
static inline uint128_t u128_not(uint128_t a)               { return ~a; }
static inline uint128_t u128_shl(uint128_t a, int n)        { return a << n; }
static inline uint128_t u128_shr(uint128_t a, int n)        { return a >> n; }

static inline uint128_t u128_mul_u64(uint128_t a, uint64_t b) { return a * b; }
static inline uint128_t u128_div10(uint128_t a) { return a / 10; }
static inline int       u128_mod10(uint128_t a) { return (int)(a % 10); }

#else /* !__SIZEOF_INT128__ */

/* ================================================================
 * Portable 128-bit path (32-bit platforms without __uint128_t).
 * Uses a struct of two uint64_t with explicit arithmetic.
 * ================================================================ */

/* Field order matches the byte layout of a native __uint128_t on the same
 * endianness, so that an array of these structs is binary-compatible with one
 * of native __uint128_t. This lets binary ipset files written by a 64-bit
 * (native) build load correctly on a 32-bit (portable) build of the same
 * endianness, and vice versa. All code refers to the fields by name, so the
 * order is irrelevant to arithmetic; only the in-memory layout changes. */
#if defined(__BYTE_ORDER__) && __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
typedef struct { uint64_t hi; uint64_t lo; } uint128_t;
#else
typedef struct { uint64_t lo; uint64_t hi; } uint128_t;
#endif

#define U128_ZERO ((uint128_t){ .hi = 0, .lo = 0 })
#define U128_ONE  ((uint128_t){ .hi = 0, .lo = 1 })
#define U128_MAX  ((uint128_t){ .hi = UINT64_MAX, .lo = UINT64_MAX })

static inline uint128_t u128_from_u64(uint64_t v) {
    uint128_t r = { .hi = 0, .lo = v };
    return r;
}

static inline uint128_t u128_from_u32(uint32_t v) {
    uint128_t r = { .hi = 0, .lo = (uint64_t)v };
    return r;
}

static inline uint64_t u128_hi64(uint128_t a) { return a.hi; }
static inline uint64_t u128_lo64(uint128_t a) { return a.lo; }

static inline int u128_is_zero(uint128_t a) {
    return a.hi == 0 && a.lo == 0;
}

static inline int u128_eq(uint128_t a, uint128_t b) {
    return a.hi == b.hi && a.lo == b.lo;
}

static inline int u128_lt(uint128_t a, uint128_t b) {
    return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo);
}

static inline int u128_gt(uint128_t a, uint128_t b) {
    return a.hi > b.hi || (a.hi == b.hi && a.lo > b.lo);
}

static inline int u128_le(uint128_t a, uint128_t b) { return !u128_gt(a, b); }
static inline int u128_ge(uint128_t a, uint128_t b) { return !u128_lt(a, b); }

static inline uint128_t u128_add(uint128_t a, uint128_t b) {
    uint128_t r;
    r.lo = a.lo + b.lo;
    r.hi = a.hi + b.hi + (r.lo < a.lo);
    return r;
}

static inline uint128_t u128_sub(uint128_t a, uint128_t b) {
    uint128_t r;
    r.lo = a.lo - b.lo;
    r.hi = a.hi - b.hi - (a.lo < b.lo);
    return r;
}

static inline uint128_t u128_inc(uint128_t a) {
    uint128_t r;
    r.lo = a.lo + 1;
    r.hi = a.hi + (r.lo == 0);
    return r;
}

static inline uint128_t u128_dec(uint128_t a) {
    uint128_t r;
    r.hi = a.hi - (a.lo == 0);
    r.lo = a.lo - 1;
    return r;
}

static inline uint128_t u128_and(uint128_t a, uint128_t b) {
    uint128_t r = { .hi = a.hi & b.hi, .lo = a.lo & b.lo };
    return r;
}

static inline uint128_t u128_or(uint128_t a, uint128_t b) {
    uint128_t r = { .hi = a.hi | b.hi, .lo = a.lo | b.lo };
    return r;
}

static inline uint128_t u128_not(uint128_t a) {
    uint128_t r = { .hi = ~a.hi, .lo = ~a.lo };
    return r;
}

static inline uint128_t u128_shl(uint128_t a, int n) {
    uint128_t r;
    if(n == 0)   return a;
    if(n >= 128) { r.hi = 0; r.lo = 0; return r; }
    if(n >= 64) {
        r.hi = a.lo << (n - 64);
        r.lo = 0;
    }
    else {
        r.hi = (a.hi << n) | (a.lo >> (64 - n));
        r.lo = a.lo << n;
    }
    return r;
}

static inline uint128_t u128_shr(uint128_t a, int n) {
    uint128_t r;
    if(n == 0)   return a;
    if(n >= 128) { r.hi = 0; r.lo = 0; return r; }
    if(n >= 64) {
        r.lo = a.hi >> (n - 64);
        r.hi = 0;
    }
    else {
        r.lo = (a.lo >> n) | (a.hi << (64 - n));
        r.hi = a.hi >> n;
    }
    return r;
}

/* multiply uint128 by uint64, keeping low 128 bits */
static inline uint128_t u128_mul_u64(uint128_t a, uint64_t b) {
    uint32_t al = (uint32_t)a.lo;
    uint32_t ah = (uint32_t)(a.lo >> 32);
    uint32_t bl = (uint32_t)b;
    uint32_t bh = (uint32_t)(b >> 32);
    uint64_t p0, p1, p2, p3, carry;
    uint128_t r;

    p0 = (uint64_t)al * bl;
    p1 = (uint64_t)al * bh;
    p2 = (uint64_t)ah * bl;
    p3 = (uint64_t)ah * bh;

    carry = (p0 >> 32) + (uint32_t)p1 + (uint32_t)p2;
    r.lo = ((uint64_t)(uint32_t)p0) | (carry << 32);
    r.hi = p3 + (p1 >> 32) + (p2 >> 32) + (carry >> 32) + a.hi * b;
    return r;
}

/* long division by 10 using 32-bit digits */
static inline uint128_t u128_div10(uint128_t a) {
    uint128_t q;
    uint32_t d3, d2, d1, d0, q3, q2, q1, q0;
    uint64_t r, tmp;

    d3 = (uint32_t)(a.hi >> 32);
    d2 = (uint32_t)a.hi;
    d1 = (uint32_t)(a.lo >> 32);
    d0 = (uint32_t)a.lo;

    tmp = (uint64_t)d3;         q3 = (uint32_t)(tmp / 10); r = tmp % 10;
    tmp = (r << 32) | d2;       q2 = (uint32_t)(tmp / 10); r = tmp % 10;
    tmp = (r << 32) | d1;       q1 = (uint32_t)(tmp / 10); r = tmp % 10;
    tmp = (r << 32) | d0;       q0 = (uint32_t)(tmp / 10);

    q.hi = ((uint64_t)q3 << 32) | q2;
    q.lo = ((uint64_t)q1 << 32) | q0;
    return q;
}

static inline int u128_mod10(uint128_t a) {
    uint64_t r;
    r = ((uint64_t)(uint32_t)(a.hi >> 32)) % 10;
    r = ((r << 32) | (uint32_t)a.hi) % 10;
    r = ((r << 32) | (uint32_t)(a.lo >> 32)) % 10;
    r = ((r << 32) | (uint32_t)a.lo) % 10;
    return (int)r;
}

#endif /* __SIZEOF_INT128__ */

#endif /* IPRANGE_UINT128_H */
