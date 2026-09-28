#include "iprange_v4.h"

#include <stdint.h>
#include <stdio.h>
#include <string.h>

/* The portable subset of the C error-code contract: the two codes that
 * a path-free immutable open can reach on every qualified host.  Live
 * entries and POSIX path-kind scoping stay in the platform callers,
 * because FreeBSD refuses live before any of this runs.
 */

#define CHECK(expression)                                                       \
    do {                                                                        \
        if (!(expression)) {                                                    \
            fprintf(stderr, "check failed at %s:%d: %s\n", __FILE__, __LINE__, \
                    #expression);                                               \
            return 1;                                                           \
        }                                                                       \
    } while (0)

static uint32_t read_code(iprange_v4_abi1_error *error, uint32_t *code)
{
    uint8_t caller_present = 0;
    uint64_t caller_code = 0;
    return iprange_v4_abi1_error_code(error, code, &caller_present, &caller_code);
}

int main(void)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_path path = {0};
    uint32_t code = 0;

    CHECK(iprange_v4_abi1_version() == IPRANGE_V4_ABI1_ABI_VERSION);

    path.kind = IPRANGE_V4_ABI1_PATH_POSIX_BYTES;
    path.pointer = NULL;
    path.length = 1;
    CHECK(iprange_v4_abi1_open_immutable_reader(path, &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(reader == NULL);
    CHECK(read_code(error, &code) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_NULL_POINTER);
    CHECK(iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK);
    error = NULL;

    path.pointer = "";
    path.length = 0;
    CHECK(iprange_v4_abi1_open_immutable_reader(path, &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(read_code(error, &code) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_INVALID_LENGTH);
    CHECK(iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK);

    printf("portable errors ok\n");
    return 0;
}
