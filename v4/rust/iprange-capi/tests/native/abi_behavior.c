#define _GNU_SOURCE

#include "iprange_v4.h"

#include <dirent.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

extern uint32_t iprange_v4_native_test_panic(uint32_t *error_code);

#define CHECK(condition)                                                        \
    do {                                                                        \
        if (!(condition)) {                                                     \
            fprintf(stderr, "check failed at line %d: %s\n", __LINE__, #condition); \
            return 1;                                                           \
        }                                                                       \
    } while (0)

static iprange_v4_abi1_cancellation no_cancellation(void)
{
    iprange_v4_abi1_cancellation value = {0};
    return value;
}

static iprange_v4_abi1_path path_from(const char *value)
{
    iprange_v4_abi1_path path = {0};
    path.kind = IPRANGE_V4_ABI1_PATH_POSIX_BYTES;
    path.pointer = value;
    path.length = (uint64_t)strlen(value);
    return path;
}

static iprange_v4_abi1_ip ipv4(uint32_t value)
{
    iprange_v4_abi1_ip address = {0};
    address.family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    address.bytes[0] = (uint8_t)(value >> 24);
    address.bytes[1] = (uint8_t)(value >> 16);
    address.bytes[2] = (uint8_t)(value >> 8);
    address.bytes[3] = (uint8_t)value;
    return address;
}

static int is_zero(const void *pointer, size_t length)
{
    const uint8_t *bytes = pointer;
    size_t index;
    for (index = 0; index < length; ++index) {
        if (bytes[index] != 0) {
            return 0;
        }
    }
    return 1;
}

typedef struct cleanup_fault {
    const char *sidecar;
    int target_fd;
    int backup_fd;
    int injected;
} cleanup_fault;

typedef struct rename_fault {
    const char *sidecar;
    const char *saved;
    int injected;
} rename_fault;

static uint64_t load_u64_le(const uint8_t *bytes)
{
    uint64_t value = 0;
    unsigned int index;
    for (index = 0; index < 8; ++index) {
        value |= (uint64_t)bytes[index] << (index * 8);
    }
    return value;
}

static int sidecar_has_active_reader(const char *path)
{
    uint8_t slot[16];
    unsigned int index;
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        return 0;
    }
    for (index = 0; index < 2; ++index) {
        ssize_t length = pread(fd, slot, sizeof(slot), 4096 + (off_t)(index * 16));
        if (length == (ssize_t)sizeof(slot)) {
            uint64_t transaction = load_u64_le(slot);
            uint64_t complement = load_u64_le(slot + 8);
            if (transaction != 0 && complement == ~transaction) {
                close(fd);
                return 1;
            }
        }
    }
    close(fd);
    return 0;
}

static void inject_sidecar_failure(cleanup_fault *fault)
{
    struct dirent *entry;
    DIR *directory = opendir("/proc/self/fd");
    if (directory == NULL) {
        return;
    }
    while ((entry = readdir(directory)) != NULL) {
        char fd_path[64];
        char target[4096];
        char *end = NULL;
        long parsed = strtol(entry->d_name, &end, 10);
        int fd;
        int poison_fd;
        ssize_t length;
        if (end == entry->d_name || *end != '\0' || parsed < 0 ||
            parsed > 0x7fffffffL) {
            continue;
        }
        fd = (int)parsed;
        if (fd == dirfd(directory)) {
            continue;
        }
        if (snprintf(fd_path, sizeof(fd_path), "/proc/self/fd/%d", fd) < 0) {
            continue;
        }
        length = readlink(fd_path, target, sizeof(target) - 1);
        if (length < 0 || (size_t)length >= sizeof(target)) {
            continue;
        }
        target[length] = '\0';
        if (strcmp(target, fault->sidecar) != 0) {
            continue;
        }
        fault->backup_fd = fcntl(fd, F_DUPFD_CLOEXEC, 64);
        /* Keep the Rust-owned descriptor valid while making record locks fail. */
        poison_fd = open("/dev/null", O_PATH | O_CLOEXEC);
        if (fault->backup_fd < 0 || poison_fd < 0 ||
            dup2(poison_fd, fd) != fd) {
            if (poison_fd >= 0) {
                close(poison_fd);
            }
            if (fault->backup_fd >= 0) {
                close(fault->backup_fd);
                fault->backup_fd = -1;
            }
            break;
        }
        close(poison_fd);
        if (fcntl(fd, F_SETFD, FD_CLOEXEC) != 0) {
            (void)dup2(fault->backup_fd, fd);
            close(fault->backup_fd);
            fault->backup_fd = -1;
            break;
        }
        fault->target_fd = fd;
        fault->injected = 1;
        break;
    }
    closedir(directory);
}

static uint8_t fail_source_cleanup_once(void *context)
{
    cleanup_fault *fault = context;
    if (!fault->injected && sidecar_has_active_reader(fault->sidecar)) {
        inject_sidecar_failure(fault);
    }
    return 0;
}

static uint8_t fail_worker_source_cleanup_once(void *context)
{
    rename_fault *fault = context;
    if (!fault->injected && sidecar_has_active_reader(fault->sidecar) &&
        rename(fault->sidecar, fault->saved) == 0) {
        fault->injected = 1;
    }
    return 0;
}

static int restore_sidecar(cleanup_fault *fault)
{
    CHECK(fault->injected == 1);
    CHECK(fault->target_fd >= 0);
    CHECK(fault->backup_fd >= 0);
    CHECK(dup2(fault->backup_fd, fault->target_fd) == fault->target_fd);
    CHECK(fcntl(fault->target_fd, F_SETFD, FD_CLOEXEC) == 0);
    CHECK(close(fault->backup_fd) == 0);
    fault->backup_fd = -1;
    return 0;
}

static uint32_t inspect_error(iprange_v4_abi1_error *error,
                              uint8_t *caller_present,
                              uint64_t *caller_code)
{
    uint32_t code = 0;
    CHECK(error != NULL);
    CHECK(iprange_v4_abi1_error_code(error, &code, caller_present, caller_code) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return code;
}

static int destroy_error(iprange_v4_abi1_error *error)
{
    CHECK(iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

static int destroy_report(iprange_v4_abi1_report *report)
{
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_report_destroy(report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}

typedef struct source_state {
    uint32_t emitted;
} source_state;

static uint32_t good_source(void *context,
                            iprange_v4_abi1_direct_range *records,
                            uint64_t capacity,
                            uint64_t *count,
                            iprange_v4_abi1_callback_failure *failure)
{
    source_state *state = context;
    (void)failure;
    if (state->emitted != 0) {
        *count = 0;
        return IPRANGE_V4_ABI1_SOURCE_OUTCOME_END;
    }
    if (capacity < 2) {
        *count = 0;
        return IPRANGE_V4_ABI1_SOURCE_OUTCOME_ERROR;
    }
    records[0].range.from = ipv4(10);
    records[0].range.to = ipv4(20);
    records[0].value = 7;
    records[1].range.from = ipv4(15);
    records[1].range.to = ipv4(17);
    records[1].value = 9;
    *count = 2;
    state->emitted = 1;
    return IPRANGE_V4_ABI1_SOURCE_OUTCOME_BATCH;
}

static uint32_t failing_source(void *context,
                               iprange_v4_abi1_direct_range *records,
                               uint64_t capacity,
                               uint64_t *count,
                               iprange_v4_abi1_callback_failure *failure)
{
    static const uint8_t message[] = "native source failure";
    (void)context;
    (void)records;
    (void)capacity;
    *count = 0;
    failure->caller_code = 4242;
    failure->message_pointer = message;
    failure->message_length = sizeof(message) - 1;
    return IPRANGE_V4_ABI1_SOURCE_OUTCOME_ERROR;
}

static uint32_t stopping_sink(void *context,
                              const iprange_v4_abi1_direct_range *records,
                              uint64_t count,
                              iprange_v4_abi1_callback_failure *failure)
{
    uint64_t *seen = context;
    (void)records;
    (void)failure;
    *seen += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_STOP;
}

static uint32_t first_seen_removal_sink(
    void *context,
    const iprange_v4_abi1_first_seen_removal *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    (void)context;
    (void)records;
    (void)count;
    (void)failure;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static int expect_direct(iprange_v4_abi1_reader *reader,
                         uint32_t address,
                         uint32_t expected)
{
    iprange_v4_abi1_error *error = NULL;
    uint8_t present = 0;
    uint32_t value = 0;
    CHECK(iprange_v4_abi1_reader_lookup_direct(
              reader, ipv4(address), &present, &value, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(present == 1);
    CHECK(value == expected);
    return 0;
}

int main(int argc, char **argv)
{
    static const uint8_t tag[] = "asn";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_create_report create = {0};
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_transaction_budget budget = {0};
    iprange_v4_abi1_finish_input_report finish = {0};
    iprange_v4_abi1_commit_report commit = {0};
    iprange_v4_abi1_abort_report abort = {0};
    source_state source = {0};
    uint8_t caller_present = 0;
    uint64_t caller_code = 0;
    uint32_t code;

    CHECK(argc == 3);
    CHECK(iprange_v4_abi1_version() == IPRANGE_V4_ABI1_ABI_VERSION);

    {
        uint32_t panic_code = 0;
        CHECK(iprange_v4_native_test_panic(&panic_code) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(panic_code == IPRANGE_V4_ABI1_ERROR_CODE_PANIC);
    }

    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              value_tag,
              2,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(report != NULL);
    CHECK(iprange_v4_abi1_report_get_create(report, &create, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(create.state == IPRANGE_V4_ABI1_CREATION_CREATED);

    {
        iprange_v4_abi1_database_info malformed;
        memset(&malformed, 0xff, sizeof(malformed));
        CHECK(iprange_v4_abi1_reader_database_info(
                  (const iprange_v4_abi1_reader *)report, &malformed, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(is_zero(&malformed, sizeof(malformed)));
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_HANDLE_KIND);
        CHECK(destroy_error(error) == 0);
        error = NULL;
    }
    CHECK(destroy_report(report) == 0);
    report = NULL;

    budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    budget.struct_size = sizeof(budget);
    budget.max_heap_bytes = 2 * 1024 * 1024;
    budget.max_private_pages = 20000;
    budget.max_file_growth_pages = 20000;
    budget.max_open_files = 2;
    {
        iprange_v4_abi1_transaction_budget malformed = budget;
        malformed.reserved = 1;
        writer = (iprange_v4_abi1_writer *)(uintptr_t)1;
        CHECK(iprange_v4_abi1_open_live_writer(path_from(argv[1]),
                                               &malformed,
                                               no_cancellation(),
                                               &writer,
                                               &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(writer == NULL);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_RESERVED_NONZERO);
        CHECK(destroy_error(error) == 0);
        error = NULL;
    }
    CHECK(iprange_v4_abi1_open_live_writer(path_from(argv[1]),
                                           &budget,
                                           no_cancellation(),
                                           &writer,
                                           &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);

    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_BUSY);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_begin_membership(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_VALUE_KIND);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_begin_first_seen_refresh(
              writer, 1234, no_cancellation(), &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_VALUE_TAG);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_begin_last_seen_refresh(
              writer, 1234, 1000, no_cancellation(), &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_VALUE_TAG);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_finish_first_seen_with_removals(
              writer, first_seen_removal_sink, NULL, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_STATE);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_add_direct_ranges(
              writer, failing_source, NULL, &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    code = inspect_error(error, &caller_present, &caller_code);
    CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_SOURCE_FAILED);
    CHECK(caller_present == 1);
    CHECK(caller_code == 4242);
    {
        static const char expected[] = "direct source failed: native source failure";
        uint8_t message[64] = {0};
        uint64_t required = 0;
        iprange_v4_abi1_mutable_byte_slice output = {
            message,
            sizeof(message),
        };
        CHECK(iprange_v4_abi1_error_message_query(error, &required) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(required == sizeof(expected) - 1);
        CHECK(iprange_v4_abi1_error_message_read(error, output, &required) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(memcmp(message, expected, sizeof(expected) - 1) == 0);
    }
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_abort(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_abort(report, &abort, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(abort.outcome == IPRANGE_V4_ABI1_ABORT_OUTCOME_ABORTED);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_add_direct_ranges(
              writer, good_source, &source, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_finish_input(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_finish_input(report, &finish, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish.input_record_count == 2);
    CHECK(finish.after_range_record_count == 3);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_commit(report, &commit, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(commit.durability == IPRANGE_V4_ABI1_COMMIT_DURABILITY_COMMITTED);
    CHECK(commit.cleanup_state == IPRANGE_V4_ABI1_CLEANUP_STATE_CLEAN);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    {
        iprange_v4_abi1_close_report close = {0};
        CHECK(iprange_v4_abi1_report_get_close(report, &close, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(close.outcome == IPRANGE_V4_ABI1_CLOSE_OUTCOME_CLOSED);
        CHECK(close.abort_present == 1);
        CHECK(close.abort_outcome == IPRANGE_V4_ABI1_ABORT_OUTCOME_ABORTED);
        CHECK(close.cleanup_state == IPRANGE_V4_ABI1_CLEANUP_STATE_CLEAN);
    }
    CHECK(destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    writer = NULL;

    CHECK(iprange_v4_abi1_open_live_reader(path_from(argv[1]),
                                           no_cancellation(),
                                           &reader,
                                           &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    {
        uint8_t storage[sizeof(iprange_v4_abi1_database_info) +
                        _Alignof(iprange_v4_abi1_database_info)];
        uintptr_t start = (uintptr_t)storage;
        uintptr_t aligned =
            (start + _Alignof(iprange_v4_abi1_database_info) - 1) &
            ~(uintptr_t)(_Alignof(iprange_v4_abi1_database_info) - 1);
        iprange_v4_abi1_database_info *misaligned =
            (iprange_v4_abi1_database_info *)(aligned + 1);
        CHECK(iprange_v4_abi1_reader_database_info(reader, misaligned, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_MISALIGNED_POINTER);
        CHECK(destroy_error(error) == 0);
        error = NULL;
    }
    {
        uint64_t seen = 0;
        CHECK(iprange_v4_abi1_reader_scan_direct(reader,
                                                 99,
                                                 NULL,
                                                 no_cancellation(),
                                                 stopping_sink,
                                                 &seen,
                                                 &report,
                                                 &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(report == NULL);
        CHECK(seen == 0);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ENUM);
        CHECK(destroy_error(error) == 0);
        error = NULL;
    }
    CHECK(expect_direct(reader, 14, 7) == 0);
    CHECK(expect_direct(reader, 16, 9) == 0);
    CHECK(expect_direct(reader, 18, 7) == 0);

    {
        iprange_v4_abi1_ip wrong_family = {0};
        uint8_t present = 0xff;
        uint32_t value = 0xffffffffu;
        wrong_family.family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV6;
        CHECK(iprange_v4_abi1_reader_lookup_direct(
                  reader, wrong_family, &present, &value, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(present == 0);
        CHECK(value == 0);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_ADDRESS_FAMILY);
        CHECK(destroy_error(error) == 0);
        error = NULL;
    }

    {
        uint64_t seen = 0;
        iprange_v4_abi1_scan_report scan = {0};
        CHECK(iprange_v4_abi1_reader_scan_direct(reader,
                                                 IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD,
                                                 NULL,
                                                 no_cancellation(),
                                                 stopping_sink,
                                                 &seen,
                                                 &report,
                                                 &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_STOPPED_BY_SINK);
        CHECK(destroy_error(error) == 0);
        error = NULL;
        CHECK(iprange_v4_abi1_report_get_scan(report, &scan, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(scan.record_count == 3);
        CHECK(scan.completed == 0);
        CHECK(seen == 3);
        CHECK(destroy_report(report) == 0);
        report = NULL;
    }
    CHECK(iprange_v4_abi1_reader_close(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    reader = NULL;

    {
        size_t sidecar_length = strlen(argv[1]) + sizeof(".readers");
        size_t saved_length = sidecar_length + sizeof(".saved") - 1;
        char *sidecar = malloc(sidecar_length);
        char *saved = malloc(saved_length);
        rename_fault fault;
        iprange_v4_abi1_cancellation cancellation = {0};
        iprange_v4_abi1_validation_budget validation = {0};
        iprange_v4_abi1_cleanup_guard *guard = NULL;
        uint8_t changed = 0xff;
        CHECK(sidecar != NULL);
        CHECK(saved != NULL);
        CHECK(snprintf(sidecar, sidecar_length, "%s.readers", argv[1]) > 0);
        CHECK(snprintf(saved, saved_length, "%s.saved", sidecar) > 0);
        fault.sidecar = sidecar;
        fault.saved = saved;
        fault.injected = 0;
        cancellation.callback = fail_worker_source_cleanup_once;
        cancellation.context = &fault;
        validation.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        validation.struct_size = sizeof(validation);
        validation.max_heap_bytes = 4 * 1024 * 1024;
        validation.max_open_files = 2;

        CHECK(iprange_v4_abi1_validate(
                  path_from(argv[1]),
                  IPRANGE_V4_ABI1_VALIDATION_MODE_LIVE_CURRENT,
                  NULL,
                  0,
                  &validation,
                  cancellation,
                  NULL,
                  NULL,
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(fault.injected == 1);
        CHECK(report != NULL);
        CHECK(error != NULL);
        CHECK(destroy_error(error) == 0);
        error = NULL;
        CHECK(iprange_v4_abi1_report_destroy(report, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_BUSY);
        CHECK(destroy_error(error) == 0);
        error = NULL;
        CHECK(iprange_v4_abi1_report_take_cleanup_guard(report, &guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(guard != NULL);
        CHECK(error == NULL);
        CHECK(destroy_report(report) == 0);
        report = NULL;

        CHECK(rename(saved, sidecar) == 0);
        CHECK(iprange_v4_abi1_cleanup_guard_retry(guard, &changed, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(changed == 1);
        CHECK(iprange_v4_abi1_cleanup_guard_close(guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cleanup_guard_destroy(guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        free(saved);
        free(sidecar);
    }

    {
        size_t sidecar_length = strlen(argv[1]) + sizeof(".readers");
        size_t destination_length = strlen(argv[2]) + sizeof(".fault");
        char *sidecar = malloc(sidecar_length);
        char *destination = malloc(destination_length);
        cleanup_fault fault;
        iprange_v4_abi1_cancellation cancellation = {0};
        iprange_v4_abi1_snapshot_budget snapshot = {0};
        iprange_v4_abi1_cleanup_guard *guard = NULL;
        uint8_t changed = 0xff;
        CHECK(sidecar != NULL);
        CHECK(destination != NULL);
        CHECK(snprintf(sidecar, sidecar_length, "%s.readers", argv[1]) > 0);
        CHECK(snprintf(destination, destination_length, "%s.fault", argv[2]) > 0);
        fault.sidecar = sidecar;
        fault.target_fd = -1;
        fault.backup_fd = -1;
        fault.injected = 0;
        cancellation.callback = fail_source_cleanup_once;
        cancellation.context = &fault;
        snapshot.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        snapshot.struct_size = sizeof(snapshot);
        snapshot.max_heap_bytes = 4 * 1024 * 1024;
        snapshot.max_output_pages = 20000;
        snapshot.max_open_files = 4;

        CHECK(iprange_v4_abi1_snapshot_to(
                  path_from(argv[1]),
                  IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
                  path_from(destination),
                  IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
                  &snapshot,
                  cancellation,
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(fault.injected == 1);
        CHECK(report != NULL);
        CHECK(error != NULL);
        CHECK(destroy_error(error) == 0);
        error = NULL;
        CHECK(iprange_v4_abi1_report_destroy(report, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_BUSY);
        CHECK(destroy_error(error) == 0);
        error = NULL;
        CHECK(iprange_v4_abi1_report_take_cleanup_guard(report, &guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(guard != NULL);
        CHECK(error == NULL);
        CHECK(destroy_report(report) == 0);
        report = NULL;

        CHECK(iprange_v4_abi1_cleanup_guard_destroy(guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        code = inspect_error(error, &caller_present, &caller_code);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_HANDLE_BUSY);
        CHECK(destroy_error(error) == 0);
        error = NULL;

        CHECK(restore_sidecar(&fault) == 0);
        CHECK(iprange_v4_abi1_cleanup_guard_retry(guard, &changed, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(changed == 1);
        changed = 0xff;
        CHECK(iprange_v4_abi1_cleanup_guard_retry(guard, &changed, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(changed == 0);
        CHECK(iprange_v4_abi1_cleanup_guard_close(guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cleanup_guard_destroy(guard, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        free(destination);
        free(sidecar);
    }

    {
        iprange_v4_abi1_snapshot_budget snapshot = {0};
        iprange_v4_abi1_publication_report publication = {0};
        snapshot.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        snapshot.struct_size = sizeof(snapshot);
        snapshot.max_heap_bytes = 4 * 1024 * 1024;
        snapshot.max_output_pages = 20000;
        snapshot.max_open_files = 4;
        CHECK(iprange_v4_abi1_snapshot_to(
                  path_from(argv[1]),
                  IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
                  path_from(argv[2]),
                  IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
                  &snapshot,
                  no_cancellation(),
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_report_get_publication(report, &publication, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(publication.publication == IPRANGE_V4_ABI1_PUBLICATION_PUBLISHED);
        CHECK(publication.cleanup_state == IPRANGE_V4_ABI1_CLEANUP_STATE_CLEAN);
        CHECK(destroy_report(report) == 0);
    }

    return 0;
}
