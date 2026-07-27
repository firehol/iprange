#include "abi_test_support.h"

#include <fcntl.h>
#include <unistd.h>

static uint32_t finding_sink(
    void *context,
    const iprange_v4_abi1_validation_finding *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    uint64_t *seen = context;
    (void)records;
    (void)failure;
    *seen += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t unknown_sink(
    void *context,
    const iprange_v4_abi1_recovery_unknown *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    uint64_t *seen = context;
    (void)records;
    (void)failure;
    *seen += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static iprange_v4_abi1_validation_budget validation_budget(void)
{
    iprange_v4_abi1_validation_budget value = {0};
    value.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    value.struct_size = sizeof(value);
    value.max_heap_bytes = 8 * 1024 * 1024;
    value.max_open_files = 8;
    return value;
}

static iprange_v4_abi1_recovery_budget recovery_budget(void)
{
    iprange_v4_abi1_recovery_budget value = {0};
    value.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    value.struct_size = sizeof(value);
    value.max_heap_bytes = 8 * 1024 * 1024;
    value.max_output_pages = 65536;
    value.max_open_files = 8;
    return value;
}

static iprange_v4_abi1_snapshot_budget snapshot_budget(void)
{
    iprange_v4_abi1_snapshot_budget value = {0};
    value.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    value.struct_size = sizeof(value);
    value.max_heap_bytes = 8 * 1024 * 1024;
    value.max_output_pages = 65536;
    value.max_open_files = 4;
    return value;
}

static int create_direct(const char *path, iprange_v4_abi1_report **created)
{
    static const uint8_t tag[] = "asn";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              value_tag,
              4,
              no_cancellation(),
              created,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}

static int populate_direct(const char *path, iprange_v4_abi1_report **committed)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_report *finished = NULL;
    iprange_v4_abi1_report *closed = NULL;
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_direct_range record = {ipv4_range(1, 100), 42, 0};
    direct_source source = {&record, 1, 0};
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(path), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_add_direct_ranges(
              writer, direct_callback, &source, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_finish_input(writer, &finished, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(finished) == 0);
    CHECK(iprange_v4_abi1_writer_commit(writer, committed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_close(writer, &closed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(closed) == 0);
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

static int check_empty_report_collections(iprange_v4_abi1_report *report)
{
    iprange_v4_abi1_error *error = NULL;
    const iprange_v4_abi1_error *cause = (const iprange_v4_abi1_error *)1;
    iprange_v4_abi1_cleanup_artifact cleanup = {0};
    iprange_v4_abi1_housekeeping_artifact housekeeping = {0};
    uint64_t count = UINT64_MAX;
    CHECK(iprange_v4_abi1_report_cause(report, &cause, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(cause == NULL && error == NULL);
    CHECK(iprange_v4_abi1_report_cleanup_artifact_count(
              report, &count, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(count == 0);
    CHECK(iprange_v4_abi1_report_cleanup_artifact_get(
              report, 0, &cleanup, &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(error != NULL && destroy_error(error) == 0);
    error = NULL;
    CHECK(iprange_v4_abi1_report_housekeeping_artifact_count(
              report, &count, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(count == 0);
    CHECK(iprange_v4_abi1_report_housekeeping_artifact_get(
              report, 0, &housekeeping, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(error != NULL && destroy_error(error) == 0);
    return 0;
}

static int validate_source(const char *path,
                           uint32_t mode,
                           iprange_v4_abi1_report *candidates,
                           uint64_t candidate_index)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_validation_report facts = {0};
    iprange_v4_abi1_validation_budget budget = validation_budget();
    uint64_t findings = 0;
    CHECK(iprange_v4_abi1_validate(
              path_from(path),
              mode,
              candidates,
              candidate_index,
              &budget,
              no_cancellation(),
              finding_sink,
              &findings,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_validation(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.valid == 1 && findings == 0);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int inspect_candidates(const char *path,
                              uint32_t source_mode,
                              iprange_v4_abi1_report **report)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_validation_budget budget = validation_budget();
    iprange_v4_abi1_recovery_candidates_report facts = {0};
    iprange_v4_abi1_recovery_candidate candidate = {0};
    uint64_t count = 0;
    CHECK(iprange_v4_abi1_inspect_recovery_candidates(
              path_from(path),
              source_mode,
              &budget,
              no_cancellation(),
              report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_recovery_candidates(
              *report, &facts, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_recovery_candidate_count(
              *report, &count, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(count > 0);
    CHECK(iprange_v4_abi1_report_recovery_candidate_get(
              *report, 0, &candidate, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(candidate.abi_version == IPRANGE_V4_ABI1_ABI_VERSION);
    return 0;
}

static int recover_source(const char *source,
                          uint32_t mode,
                          iprange_v4_abi1_report *candidates,
                          const char *destination)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_recovery_report facts = {0};
    iprange_v4_abi1_recovery_budget budget = recovery_budget();
    uint64_t unknowns = 0;
    uint32_t status;
    if (mode == IPRANGE_V4_ABI1_OPEN_MODE_IMMUTABLE) {
        status = iprange_v4_abi1_recover_immutable(
            path_from(source),
            candidates,
            0,
            path_from(destination),
            &budget,
            no_cancellation(),
            unknown_sink,
            &unknowns,
            &report,
            &error);
    } else if (mode == IPRANGE_V4_ABI1_OPEN_MODE_LIVE) {
        status = iprange_v4_abi1_recover_live(
            path_from(source),
            candidates,
            0,
            path_from(destination),
            &budget,
            no_cancellation(),
            unknown_sink,
            &unknowns,
            &report,
            &error);
    } else {
        status = iprange_v4_abi1_recover_offline(
            path_from(source),
            candidates,
            0,
            path_from(destination),
            &budget,
            no_cancellation(),
            unknown_sink,
            &unknowns,
            &report,
            &error);
    }
    CHECK(status == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_report_get_recovery(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.publication.publication ==
          IPRANGE_V4_ABI1_PUBLICATION_PUBLISHED);
    CHECK(unknowns == 0);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int write_bytes(const char *path, const char *content)
{
    int descriptor = open(path, O_CREAT | O_EXCL | O_WRONLY, 0600);
    size_t length = strlen(content);
    if (descriptor < 0 ||
        write(descriptor, content, length) != (ssize_t)length ||
        close(descriptor) != 0) {
        return 1;
    }
    return 0;
}

static int exercise_residue(const char *path)
{
    char coordination[4096];
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_report *removal = NULL;
    iprange_v4_abi1_residue *residue = NULL;
    iprange_v4_abi1_residue_report facts = {0};
    CHECK(snprintf(coordination, sizeof(coordination), "%s.readers", path) > 0);
    CHECK(write_bytes(path, "ordinary main") == 0);
    CHECK(write_bytes(coordination, "malformed coordination") == 0);
    CHECK(iprange_v4_abi1_inspect_publication_residue(
              path_from(path), no_cancellation(), &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_residue(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_take_residue(report, &residue, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(residue != NULL && destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_remove_publication_residue(
              residue, no_cancellation(), &removal, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_residue(removal, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.cleanup_state == IPRANGE_V4_ABI1_CLEANUP_STATE_CLEAN);
    CHECK(destroy_report(removal) == 0);
    CHECK(iprange_v4_abi1_residue_destroy(residue, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(write_bytes(coordination, "second malformed coordination") == 0);
    CHECK(iprange_v4_abi1_inspect_publication_residue(
              path_from(path), no_cancellation(), &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_take_residue(report, &residue, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(iprange_v4_abi1_residue_close(residue, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_residue_destroy(residue, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

int main(int argc, char **argv)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *created = NULL;
    iprange_v4_abi1_report *creation_resolution = NULL;
    iprange_v4_abi1_report *committed = NULL;
    iprange_v4_abi1_report *commit_resolution = NULL;
    iprange_v4_abi1_report *publication = NULL;
    iprange_v4_abi1_report *publication_resolution = NULL;
    iprange_v4_abi1_report *immutable_candidates = NULL;
    iprange_v4_abi1_report *live_candidates = NULL;
    iprange_v4_abi1_report *offline_candidates = NULL;
    iprange_v4_abi1_report *transition = NULL;
    iprange_v4_abi1_report *transition_resolution = NULL;
    iprange_v4_abi1_report *live_residue = NULL;
    iprange_v4_abi1_create_report create_facts = {0};
    iprange_v4_abi1_commit_resolution_report commit_facts = {0};
    iprange_v4_abi1_live_transition_report transition_facts = {0};
    iprange_v4_abi1_live_residue_report live_residue_facts = {0};
    iprange_v4_abi1_snapshot_budget snapshot = snapshot_budget();
    iprange_v4_abi1_error *io_error = NULL;
    iprange_v4_abi1_reader *missing_reader = (iprange_v4_abi1_reader *)1;
    uint8_t os_present = 0;
    int64_t os_code_value = 0;
    const iprange_v4_abi1_error *cause = (const iprange_v4_abi1_error *)1;
    iprange_v4_abi1_cleanup_guard *guard = NULL;
    iprange_v4_abi1_cleanup_artifact artifact = {0};
    uint64_t cleanup_count = UINT64_MAX;

    CHECK(argc == 8);
    CHECK(create_direct(argv[1], &created) == 0);
    CHECK(iprange_v4_abi1_report_get_create(created, &create_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_resolve_create_live(
              path_from(argv[1]),
              created,
              IPRANGE_V4_ABI1_RESOLVER_ACTION_COMPLETE,
              no_cancellation(),
              &creation_resolution,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_create_resolution(
              creation_resolution, &create_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(creation_resolution) == 0);
    CHECK(destroy_report(created) == 0);

    CHECK(populate_direct(argv[1], &committed) == 0);
    CHECK(check_empty_report_collections(committed) == 0);
    CHECK(iprange_v4_abi1_resolve_commit(
              path_from(argv[1]),
              committed,
              IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
              no_cancellation(),
              &commit_resolution,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_commit_resolution(
              commit_resolution, &commit_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(commit_resolution) == 0);
    CHECK(destroy_report(committed) == 0);

    CHECK(iprange_v4_abi1_snapshot_to(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
              path_from(argv[2]),
              IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
              &snapshot,
              no_cancellation(),
              &publication,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_resolve_publication(
              path_from(argv[2]),
              publication,
              IPRANGE_V4_ABI1_RESOLVER_ACTION_COMPLETE,
              no_cancellation(),
              &publication_resolution,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(publication_resolution) == 0);
    CHECK(destroy_report(publication) == 0);

    CHECK(validate_source(
              argv[1], IPRANGE_V4_ABI1_VALIDATION_MODE_LIVE_CURRENT, NULL, 0) ==
          0);
    CHECK(validate_source(
              argv[2], IPRANGE_V4_ABI1_VALIDATION_MODE_IMMUTABLE_CURRENT, NULL, 0) ==
          0);
    CHECK(inspect_candidates(
              argv[2], IPRANGE_V4_ABI1_OPEN_MODE_IMMUTABLE, &immutable_candidates) ==
          0);
    CHECK(validate_source(
              argv[2],
              IPRANGE_V4_ABI1_VALIDATION_MODE_OFFLINE_CANDIDATE,
              immutable_candidates,
              0) == 0);
    CHECK(recover_source(
              argv[2],
              IPRANGE_V4_ABI1_OPEN_MODE_IMMUTABLE,
              immutable_candidates,
              argv[3]) == 0);
    CHECK(destroy_report(immutable_candidates) == 0);

    CHECK(inspect_candidates(
              argv[1], IPRANGE_V4_ABI1_OPEN_MODE_LIVE, &live_candidates) == 0);
    CHECK(recover_source(
              argv[1],
              IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
              live_candidates,
              argv[4]) == 0);
    CHECK(destroy_report(live_candidates) == 0);
    CHECK(inspect_candidates(
              argv[1], IPRANGE_V4_ABI1_OPEN_MODE_OFFLINE, &offline_candidates) ==
          0);
    CHECK(recover_source(
              argv[1],
              IPRANGE_V4_ABI1_OPEN_MODE_OFFLINE,
              offline_candidates,
              argv[5]) == 0);
    CHECK(destroy_report(offline_candidates) == 0);

    CHECK(iprange_v4_abi1_initialize_live(
              path_from(argv[3]),
              4,
              no_cancellation(),
              &transition,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_live_transition(
              transition, &transition_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_resolve_live_transition(
              path_from(argv[3]),
              transition,
              IPRANGE_V4_ABI1_RESOLVER_ACTION_COMPLETE,
              no_cancellation(),
              &transition_resolution,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_live_transition_resolution(
              transition_resolution, &transition_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(transition_resolution) == 0);
    CHECK(destroy_report(transition) == 0);

    CHECK(iprange_v4_abi1_reset_live_coordination(
              path_from(argv[3]),
              5,
              IPRANGE_V4_ABI1_LIVE_RESET_POLICY_ROLLBACK_SAFE,
              no_cancellation(),
              &transition,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_live_transition(
              transition, &transition_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(transition) == 0);
    CHECK(iprange_v4_abi1_resolve_interrupted_live_transition(
              path_from(argv[3]),
              IPRANGE_V4_ABI1_RESOLVER_ACTION_COMPLETE,
              no_cancellation(),
              &live_residue,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_live_residue(
              live_residue, &live_residue_facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(live_residue) == 0);

    CHECK(exercise_residue(argv[6]) == 0);

    CHECK(iprange_v4_abi1_open_immutable_reader(
              path_from(argv[7]), &missing_reader, &io_error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(missing_reader == NULL);
    CHECK(io_error != NULL);
    CHECK(iprange_v4_abi1_error_os_code(
              io_error, &os_present, &os_code_value) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(os_present == 1);
    CHECK(iprange_v4_abi1_error_cause(io_error, &cause) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(cause == NULL);
    CHECK(iprange_v4_abi1_error_cleanup_artifact_count(
              io_error, &cleanup_count) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(cleanup_count == 0);
    CHECK(iprange_v4_abi1_error_cleanup_artifact_get(
              io_error, 0, &artifact) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(iprange_v4_abi1_error_take_cleanup_guard(io_error, &guard) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(guard == NULL);
    CHECK(destroy_error(io_error) == 0);
    return 0;
}
