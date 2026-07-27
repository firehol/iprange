#include "abi_test_support.h"

typedef struct {
    uint64_t records;
    int failed;
} sink_state;

static uint32_t artifact_sink(
    void *context,
    const iprange_v4_abi1_artifact_record *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_state *state = context;
    uint64_t index;
    (void)failure;
    state->records += count;
    for (index = 0; index < count; index++) {
        if (records[index].abi_version != IPRANGE_V4_ABI1_ABI_VERSION ||
            records[index].struct_size != sizeof(records[index])) {
            state->failed = 1;
        }
    }
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t housekeeping_sink(
    void *context,
    const iprange_v4_abi1_housekeeping_record *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_state *state = context;
    (void)records;
    (void)failure;
    state->records += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static int list_artifacts(const char *directory,
                          uint32_t operation,
                          iprange_v4_abi1_local_identity *identity)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_residue_report facts = {0};
    sink_state sink = {0};
    uint32_t status;
    if (operation ==
        IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH) {
        status = iprange_v4_abi1_list_abandoned_scratch(
            path_from(directory),
            no_cancellation(),
            artifact_sink,
            &sink,
            &report,
            &error);
    } else if (
        operation ==
        IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS) {
        status = iprange_v4_abi1_list_abandoned_publication_temps(
            path_from(directory),
            no_cancellation(),
            artifact_sink,
            &sink,
            &report,
            &error);
    } else {
        status = iprange_v4_abi1_list_abandoned_reservation_artifacts(
            path_from(directory),
            no_cancellation(),
            artifact_sink,
            &sink,
            &report,
            &error);
    }
    CHECK(status == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && sink.records == 0 && sink.failed == 0);
    CHECK(iprange_v4_abi1_report_get_residue(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.operation == operation && facts.entry_count == 0);
    *identity = facts.directory_identity;
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int remove_absent(const char *directory,
                         uint32_t operation,
                         iprange_v4_abi1_local_identity directory_identity)
{
    uint8_t attempt_id[16] = {1, 2, 3, 4};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_residue_report facts = {0};
    uint32_t status;
    if (operation ==
        IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_SCRATCH) {
        status = iprange_v4_abi1_remove_abandoned_scratch(
            path_from(directory),
            directory_identity,
            attempt_id,
            9,
            directory_identity,
            no_cancellation(),
            &report,
            &error);
    } else if (
        operation ==
        IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_PUBLICATION_TEMP) {
        status = iprange_v4_abi1_remove_abandoned_publication_temp(
            path_from(directory),
            directory_identity,
            attempt_id,
            directory_identity,
            NULL,
            NULL,
            no_cancellation(),
            &report,
            &error);
    } else {
        status = iprange_v4_abi1_remove_abandoned_reservation_artifact(
            path_from(directory),
            directory_identity,
            attempt_id,
            directory_identity,
            no_cancellation(),
            &report,
            &error);
    }
    CHECK(status == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && report != NULL);
    CHECK(iprange_v4_abi1_report_get_residue(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.operation == operation);
    CHECK(destroy_report(report) == 0);
    return 0;
}

int main(int argc, char **argv)
{
    iprange_v4_abi1_local_identity identity = {0};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = (iprange_v4_abi1_report *)1;
    iprange_v4_abi1_housekeeping_payload payload = {0};
    sink_state sink = {0};
    uint8_t attempt_id[16] = {9};

    CHECK(argc == 2);
    CHECK(list_artifacts(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH,
              &identity) == 0);
    CHECK(list_artifacts(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS,
              &identity) == 0);
    CHECK(list_artifacts(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_LIST_ABANDONED_RESERVATION_ARTIFACTS,
              &identity) == 0);

    CHECK(remove_absent(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_SCRATCH,
              identity) == 0);
    CHECK(remove_absent(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_PUBLICATION_TEMP,
              identity) == 0);
    CHECK(remove_absent(
              argv[1],
              IPRANGE_V4_ABI1_RESIDUE_OPERATION_REMOVE_ABANDONED_RESERVATION_ARTIFACT,
              identity) == 0);

    CHECK(iprange_v4_abi1_list_housekeeping_artifacts(
              path_from(argv[1]),
              no_cancellation(),
              housekeeping_sink,
              &sink,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL && error != NULL);
    CHECK(error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_OS_UNSUPPORTED);
    CHECK(destroy_error(error) == 0);
    error = NULL;

    CHECK(iprange_v4_abi1_remove_housekeeping_artifact(
              path_from(argv[1]),
              identity,
              attempt_id,
              1,
              identity,
              &payload,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL && error != NULL);
    CHECK(error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_OS_UNSUPPORTED);
    CHECK(destroy_error(error) == 0);
    return 0;
}
