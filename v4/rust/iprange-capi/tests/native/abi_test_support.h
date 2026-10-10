#ifndef IPRANGE_V4_ABI1_TEST_SUPPORT_H
#define IPRANGE_V4_ABI1_TEST_SUPPORT_H

#include "iprange_v4.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(expression)                                                        \
    do {                                                                         \
        if (!(expression)) {                                                     \
            fprintf(stderr, "check failed at %s:%d: %s\n", __FILE__, __LINE__,  \
                    #expression);                                                \
            return 1;                                                            \
        }                                                                        \
    } while (0)

typedef struct {
    const iprange_v4_abi1_range *records;
    uint64_t length;
    uint64_t offset;
} coverage_source;

typedef struct {
    const iprange_v4_abi1_direct_range *records;
    uint64_t length;
    uint64_t offset;
} direct_source;

/* Strict UTF-8 to UTF-16 decoding (sol turn-2): byte-wise widening is
 * not decoding — a multibyte checkout path becomes a different Windows
 * path and cannot open the fixtures.  Malformed input (overlong forms,
 * encoded surrogates, out-of-range scalars, truncated sequences) and
 * destination overflow are REJECTED: a rejected input produces no
 * path.  Returns 0 on success, 1 on malformed input, 2 on truncation.
 * The conversion lives in the test support, not the product. */
static inline int decode_utf8_to_utf16(const char *src, uint16_t *dst,
                                       size_t cap, size_t *out_len)
{
    const unsigned char *p = (const unsigned char *)src;
    size_t n = 0;
    if (cap == 0) {
        return 2;
    }
    while (*p != '\0') {
        unsigned long code;
        int extra;
        unsigned char lead = *p++;
        int index;
        if (lead < 0x80) {
            code = lead;
            extra = 0;
        } else if ((lead & 0xe0) == 0xc0) {
            code = lead & 0x1fu;
            extra = 1;
        } else if ((lead & 0xf0) == 0xe0) {
            code = lead & 0x0fu;
            extra = 2;
        } else if ((lead & 0xf8) == 0xf0) {
            code = lead & 0x07u;
            extra = 3;
        } else {
            return 1;
        }
        for (index = 0; index < extra; index++) {
            unsigned char cont = *p++;
            if ((cont & 0xc0) != 0x80) {
                return 1;
            }
            code = (code << 6) | (cont & 0x3fu);
        }
        if (extra == 1 && code < 0x80ul) return 1;
        if (extra == 2 && code < 0x800ul) return 1;
        if (extra == 3 && code < 0x10000ul) return 1;
        if (code > 0x10fffful) return 1;
        if (code >= 0xd800ul && code <= 0xdffful) return 1;
        if (code < 0x10000ul) {
            if (cap - n < 2u) return 2;
            dst[n++] = (uint16_t)code;
        } else {
            if (cap - n < 3u) return 2;
            code -= 0x10000ul;
            dst[n++] = (uint16_t)(0xd800u + (code >> 10));
            dst[n++] = (uint16_t)(0xdc00u + (code & 0x3ffu));
        }
    }
    dst[n] = 0;
    *out_len = n;
    return 0;
}

/* The decoder's shape pin: ASCII, multibyte, a surrogate pair, and
 * every rejection class. Runs from abi_cases before the corpus walk;
 * a regression in the decoding fails the whole run. */
static inline int selftest_decode_utf8_to_utf16(void)
{
    uint16_t units[8];
    size_t length = 0;

    /* ASCII */
    CHECK(decode_utf8_to_utf16("ab", units, 8, &length) == 0);
    CHECK(length == 2 && units[0] == 'a' && units[1] == 'b' && units[2] == 0);
    /* two-byte: U+00E9 */
    CHECK(decode_utf8_to_utf16("\xc3\xa9", units, 8, &length) == 0);
    CHECK(length == 1 && units[0] == 0x00e9);
    /* three-byte: U+20AC */
    CHECK(decode_utf8_to_utf16("\xe2\x82\xac", units, 8, &length) == 0);
    CHECK(length == 1 && units[0] == 0x20ac);
    /* four-byte: U+1D11E becomes the surrogate pair D834 DD1E */
    CHECK(decode_utf8_to_utf16("\xf0\x9d\x84\x9e", units, 8, &length) == 0);
    CHECK(length == 2 && units[0] == 0xd834 && units[1] == 0xdd1e);
    /* empty input is one terminator */
    CHECK(decode_utf8_to_utf16("", units, 8, &length) == 0);
    CHECK(length == 0 && units[0] == 0);
    /* rejection: overlong NUL */
    CHECK(decode_utf8_to_utf16("\xc0\x80", units, 8, &length) == 1);
    /* rejection: truncated sequence */
    CHECK(decode_utf8_to_utf16("\xe2\x82", units, 8, &length) == 1);
    /* rejection: encoded surrogate D800 */
    CHECK(decode_utf8_to_utf16("\xed\xa0\x80", units, 8, &length) == 1);
    /* acceptance: the exact U+10FFFF boundary (4-byte form) */
    CHECK(decode_utf8_to_utf16("\xf4\x8f\xbf\xbf", units, 8, &length) == 0);
    CHECK(length == 2 && units[0] == 0xdbff && units[1] == 0xdfff);
    /* rejection: overlong three-byte form of a one-byte scalar */
    CHECK(decode_utf8_to_utf16("\xe0\x81\xa9", units, 8, &length) == 1);
    /* rejection: a low surrogate encoded directly (DC00) */
    CHECK(decode_utf8_to_utf16("\xed\xb0\x80", units, 8, &length) == 1);
    /* rejection: beyond U+10FFFF */
    CHECK(decode_utf8_to_utf16("\xf4\x90\x80\x80", units, 8, &length) == 1);
    /* rejection: invalid lead */
    CHECK(decode_utf8_to_utf16("\xff", units, 8, &length) == 1);
    /* truncation is rejected, never a shortened path (an exact fit —
     * two units plus terminator in three slots — is success) */
    CHECK(decode_utf8_to_utf16("abc", units, 3, &length) == 2);
    CHECK(decode_utf8_to_utf16("\xf0\x9d\x84\x9e", units, 2, &length) == 2);
    CHECK(decode_utf8_to_utf16("\xf0\x9d\x84\x9e", units, 3, &length) == 0);
    return 0;
}

static inline iprange_v4_abi1_path path_from(const char *path)
{
    iprange_v4_abi1_path value = {0};
    /* The ABI's platform is the DLL's, not the C runtime's: a cygwin
     * build of the fixture still talks to a native Windows library,
     * so it must widen the path the same way (_WIN32 alone is not
     * defined under the cygwin gcc). */
#if defined(_WIN32) || defined(__CYGWIN__)
    /* The Windows ABI refuses POSIX path kinds, so a caller that only
     * passes bytes cannot reach any fixture.  Decode strictly and
     * reject loudly: a wrong wide path would open the wrong file or
     * none, and a silent truncation is a fabricated path. */
    static uint16_t units[4096];
    size_t length = 0;
    if (decode_utf8_to_utf16(path, units,
                             sizeof(units) / sizeof(units[0]), &length) != 0) {
        fprintf(stderr,
                "test support: fixture path is not decodable UTF-8 or "
                "does not fit the wide buffer: %s\n", path);
        exit(1);
    }
    value.kind = IPRANGE_V4_ABI1_PATH_WINDOWS_UTF16;
    value.pointer = units;
    value.length = length;
#else
    value.kind = IPRANGE_V4_ABI1_PATH_POSIX_BYTES;
    value.pointer = path;
    value.length = strlen(path);
#endif
    return value;
}

static inline iprange_v4_abi1_cancellation no_cancellation(void)
{
    iprange_v4_abi1_cancellation value = {0};
    return value;
}

static inline iprange_v4_abi1_transaction_budget transaction_budget(void)
{
    iprange_v4_abi1_transaction_budget value = {0};
    value.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    value.struct_size = sizeof(value);
    value.max_heap_bytes = 8 * 1024 * 1024;
    value.max_private_pages = 65536;
    value.max_file_growth_pages = 65536;
    value.max_open_files = 8;
    return value;
}

static inline iprange_v4_abi1_ip ipv4(uint32_t address)
{
    iprange_v4_abi1_ip value = {0};
    value.family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    value.bytes[0] = (uint8_t)(address >> 24);
    value.bytes[1] = (uint8_t)(address >> 16);
    value.bytes[2] = (uint8_t)(address >> 8);
    value.bytes[3] = (uint8_t)address;
    return value;
}

static inline iprange_v4_abi1_range ipv4_range(uint32_t from, uint32_t to)
{
    iprange_v4_abi1_range value = {0};
    value.from = ipv4(from);
    value.to = ipv4(to);
    return value;
}

static inline uint32_t coverage_callback(
    void *context,
    iprange_v4_abi1_range *records,
    uint64_t capacity,
    uint64_t *count,
    iprange_v4_abi1_callback_failure *failure)
{
    coverage_source *source = context;
    uint64_t remaining;
    uint64_t copied;
    (void)failure;
    if (source->offset == source->length) {
        *count = 0;
        return IPRANGE_V4_ABI1_SOURCE_OUTCOME_END;
    }
    remaining = source->length - source->offset;
    copied = remaining < capacity ? remaining : capacity;
    memcpy(records, source->records + source->offset, copied * sizeof(*records));
    source->offset += copied;
    *count = copied;
    return IPRANGE_V4_ABI1_SOURCE_OUTCOME_BATCH;
}

static inline uint32_t direct_callback(
    void *context,
    iprange_v4_abi1_direct_range *records,
    uint64_t capacity,
    uint64_t *count,
    iprange_v4_abi1_callback_failure *failure)
{
    direct_source *source = context;
    uint64_t remaining;
    uint64_t copied;
    (void)failure;
    if (source->offset == source->length) {
        *count = 0;
        return IPRANGE_V4_ABI1_SOURCE_OUTCOME_END;
    }
    remaining = source->length - source->offset;
    copied = remaining < capacity ? remaining : capacity;
    memcpy(records, source->records + source->offset, copied * sizeof(*records));
    source->offset += copied;
    *count = copied;
    return IPRANGE_V4_ABI1_SOURCE_OUTCOME_BATCH;
}

static inline uint32_t error_code(iprange_v4_abi1_error *error)
{
    uint32_t code = 0;
    uint8_t caller_present = 0;
    uint64_t caller_code = 0;
    if (iprange_v4_abi1_error_code(error, &code, &caller_present, &caller_code) !=
        IPRANGE_V4_ABI1_STATUS_OK) {
        return UINT32_MAX;
    }
    return code;
}

static inline int destroy_error(iprange_v4_abi1_error *error)
{
    return iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK ? 0 : 1;
}

static inline int destroy_report(iprange_v4_abi1_report *report)
{
    iprange_v4_abi1_error *error = NULL;
    uint32_t status = iprange_v4_abi1_report_destroy(report, &error);
    if (error != NULL) {
        (void)iprange_v4_abi1_error_destroy(error);
    }
    return status == IPRANGE_V4_ABI1_STATUS_OK ? 0 : 1;
}

#endif
