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

static inline iprange_v4_abi1_path path_from(const char *path)
{
    iprange_v4_abi1_path value = {0};
    value.kind = IPRANGE_V4_ABI1_PATH_POSIX_BYTES;
    value.pointer = path;
    value.length = strlen(path);
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
