use std::env;
use std::process::Command;

use crate::scenarios::{self, ScenarioResult};

const HEADER: &str = "scenario,size,aux,work_units,emitted_units,elapsed_ns,units_per_second,alloc_calls,alloc_bytes,rss_before_kib,rss_after_kib,rss_peak_kib,fds_before,fds_after,file_logical_bytes,file_physical_bytes,range_records,feeds,private_artifacts";

#[derive(Clone)]
pub(crate) struct Case {
    pub(crate) name: String,
    pub(crate) size: usize,
    pub(crate) auxiliary: usize,
}

pub(crate) fn run() -> Result<(), String> {
    let arguments: Vec<String> = env::args()
        .skip(1)
        .filter(|argument| argument != "--bench")
        .collect();
    match arguments.first().map(String::as_str) {
        None | Some("smoke") => run_matrix(smoke_cases()),
        Some("scale") => run_matrix(scale_cases()),
        Some("local") => crate::report::run_repeated(scale_cases(), 1, 5, false),
        Some("ci") => crate::report::run_repeated(ci_cases(), 1, 3, true),
        Some("sample") => run_sample(&arguments),
        Some("case") => run_case(&arguments),
        Some("header") => {
            println!("{HEADER}");
            Ok(())
        }
        Some(other) => Err(format!(
            "unknown mode {other:?}; expected smoke, scale, local, ci, sample, or case"
        )),
    }
}

fn run_sample(arguments: &[String]) -> Result<(), String> {
    if !(4..=5).contains(&arguments.len()) {
        return Err("sample requires: sample SCENARIO SIZE AUX [SAMPLES]".to_owned());
    }
    let name = arguments[1].as_str();
    let size = arguments[2]
        .parse()
        .map_err(|_| format!("invalid size {:?}", arguments[2]))?;
    let auxiliary = arguments[3]
        .parse()
        .map_err(|_| format!("invalid auxiliary value {:?}", arguments[3]))?;
    let samples = arguments
        .get(4)
        .map_or(Ok(5usize), |value| value.parse())
        .map_err(|_| "invalid sample count".to_owned())?;
    crate::report::run_repeated(vec![case(name, size, auxiliary)], 1, samples, false)
}

fn run_matrix(cases: Vec<Case>) -> Result<(), String> {
    println!("{HEADER}");
    let executable = env::current_exe().map_err(|error| error.to_string())?;
    for case in cases {
        let output = Command::new(&executable)
            .arg("case")
            .arg(&case.name)
            .arg(case.size.to_string())
            .arg(case.auxiliary.to_string())
            .output()
            .map_err(|error| format!("start {}: {error}", case.name))?;
        if !output.status.success() {
            return Err(format!(
                "{} size={} aux={} exited {}: {}",
                case.name,
                case.size,
                case.auxiliary,
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        print!("{}", String::from_utf8_lossy(&output.stdout));
    }
    Ok(())
}

fn run_case(arguments: &[String]) -> Result<(), String> {
    if arguments.len() != 4 {
        return Err("case requires: case SCENARIO SIZE AUX".to_owned());
    }
    let size = arguments[2]
        .parse()
        .map_err(|_| format!("invalid size {:?}", arguments[2]))?;
    let auxiliary = arguments[3]
        .parse()
        .map_err(|_| format!("invalid auxiliary value {:?}", arguments[3]))?;
    let result = scenarios::run(&arguments[1], size, auxiliary)?;
    println!("{}", csv(&result));
    Ok(())
}

fn csv(result: &ScenarioResult) -> String {
    let elapsed_ns = result.measurement.elapsed.as_nanos();
    let rate = if elapsed_ns == 0 {
        0.0
    } else {
        result.work_units as f64 / result.measurement.elapsed.as_secs_f64()
    };
    format!(
        "{},{},{},{},{},{},{:.3},{},{},{},{},{},{},{},{},{},{},{},{}",
        result.name,
        result.size,
        result.auxiliary,
        result.work_units,
        result.emitted_units,
        elapsed_ns,
        rate,
        result.measurement.allocations.calls,
        result.measurement.allocations.bytes,
        optional(result.measurement.rss_before_kib),
        optional(result.measurement.rss_after_kib),
        optional(result.measurement.rss_peak_kib),
        optional(result.measurement.fds_before),
        optional(result.measurement.fds_after),
        result.file.logical,
        optional(result.file.physical),
        result.range_records,
        result.feeds,
        result.private_artifacts,
    )
}

fn optional(value: Option<u64>) -> String {
    value.map_or_else(String::new, |value| value.to_string())
}

fn smoke_cases() -> Vec<Case> {
    vec![
        case("direct-replace", 1_000, 0),
        case("direct-replace", 4_000, 0),
        case("direct-replace-v6", 4_000, 0),
        case("direct-commit", 4_000, 0),
        case("nested-overwrite", 1_000, 0),
        case("nested-overwrite", 4_000, 0),
        case("first-seen-refresh", 1_000, 0),
        case("first-seen-refresh", 4_000, 0),
        case("last-seen-refresh", 1_000, 0),
        case("last-seen-refresh", 4_000, 0),
        case("feed-replace", 1_000, 8),
        case("feed-replace", 1_000, 64),
        case("membership-import", 1_000, 64),
        case("live-membership-lookup", 4_000, 64),
        case("immutable-membership-lookup", 4_000, 64),
        case("live-membership-random-lookup", 4_000, 64),
        case("immutable-membership-random-lookup", 4_000, 64),
        case("live-feed-scan", 4_000, 64),
        case("immutable-feed-scan", 4_000, 64),
        case("live-direct-lookup", 4_000, 0),
        case("immutable-direct-lookup", 4_000, 0),
        case("live-direct-random-lookup", 4_000, 0),
        case("immutable-direct-random-lookup", 4_000, 0),
        case("structured-build-random", 4_000, 64),
        case("structured-intern", 4_000, 64),
        case("structured-assign-random", 4_000, 64),
        case("structured-commit", 4_000, 64),
        case("live-structured-scalar-random-lookup", 4_000, 64),
        case("immutable-structured-scalar-random-lookup", 4_000, 64),
        case("live-structured-threat-random-lookup", 4_000, 64),
        case("immutable-structured-threat-random-lookup", 4_000, 64),
        case("live-structured-scalar-scan", 4_000, 64),
        case("immutable-structured-scalar-scan", 4_000, 64),
        case("immutable-separate-enrichment-random-lookup", 4_000, 64),
        case("live-direct-scan", 4_000, 0),
        case("immutable-direct-scan", 4_000, 0),
        case("live-open", 4_000, 1),
        case("live-open", 4_000, 256),
        case("snapshot", 4_000, 0),
        case("live-validation", 4_000, 0),
        case("live-membership-validation", 4_000, 64),
        case("immutable-validation", 4_000, 0),
        case("immutable-feed-random", 1_000, 0),
        case("history-project", 1_000, 7),
        case("membership-matching-feeds", 1_000, 64),
        case("membership-cardinalities", 1_000, 64),
        case("membership-selected-pair", 1_000, 2),
        case("membership-all-pairs", 1_000, 8),
        case("direct-provider-join", 1_000, 1),
        case("membership-provider-join", 1_000, 1),
        case("algebra-count", 1_000, 2),
        case("algebra-compare", 1_000, 2),
        case("algebra-publish-preserve", 1_000, 2),
        case("algebra-publish-flat", 1_000, 2),
        case("update-ipsets-workflow", 1_000, 7),
    ]
}

fn scale_cases() -> Vec<Case> {
    let mut cases = Vec::new();
    for size in [10_000, 100_000, 1_000_000] {
        cases.push(case("direct-replace", size, 0));
        cases.push(case("first-seen-refresh", size, 0));
        cases.push(case("last-seen-refresh", size, 0));
    }
    cases.push(case("direct-replace-v6", 1_000_000, 0));
    cases.push(case("direct-commit", 1_000_000, 0));
    for size in [10_000, 100_000, 1_000_000] {
        cases.push(case("nested-overwrite", size, 0));
    }
    for feeds in [64, 256, 421] {
        cases.push(case("feed-replace", 10_000, feeds));
        cases.push(case("live-membership-lookup", 100_000, feeds));
        cases.push(case("immutable-membership-lookup", 100_000, feeds));
    }
    cases.push(case("feed-replace", 100_000, 421));
    cases.push(case("feed-replace", 1_000_000, 421));
    for name in [
        "feed-first-ascending",
        "feed-second-ascending",
        "feed-first-descending",
        "feed-second-descending",
        "feed-first-random",
        "feed-second-random",
        "feed-first-overlap",
        "feed-second-overlap",
    ] {
        let second = usize::from(name.starts_with("feed-second-"));
        cases.push(case(name, 1_000_000, second));
    }
    cases.push(case("membership-import", 10_000, 421));
    cases.push(case("membership-import", 100_000, 421));
    cases.push(case("membership-import", 1_000_000, 421));
    cases.push(case("live-feed-scan", 100_000, 421));
    cases.push(case("immutable-feed-scan", 100_000, 421));
    cases.push(case("live-direct-lookup", 100_000, 0));
    cases.push(case("immutable-direct-lookup", 100_000, 0));
    cases.push(case("live-direct-random-lookup", 100_000, 0));
    cases.push(case("immutable-direct-random-lookup", 100_000, 0));
    cases.push(case("live-membership-random-lookup", 100_000, 421));
    cases.push(case("immutable-membership-random-lookup", 100_000, 421));
    cases.push(case("live-direct-random-lookup", 1_000_000, 0));
    cases.push(case("immutable-direct-random-lookup", 1_000_000, 0));
    cases.push(case("live-membership-random-lookup", 1_000_000, 421));
    cases.push(case("immutable-membership-random-lookup", 1_000_000, 421));
    cases.push(case("structured-build-random", 1_000_000, 421));
    cases.push(case("structured-intern", 65_536, 421));
    cases.push(case("structured-assign-random", 1_000_000, 421));
    cases.push(case("structured-commit", 1_000_000, 421));
    cases.push(case("live-structured-scalar-random-lookup", 1_000_000, 421));
    cases.push(case(
        "immutable-structured-scalar-random-lookup",
        1_000_000,
        421,
    ));
    cases.push(case("live-structured-threat-random-lookup", 1_000_000, 421));
    cases.push(case(
        "immutable-structured-threat-random-lookup",
        1_000_000,
        421,
    ));
    cases.push(case("live-structured-scalar-scan", 1_000_000, 421));
    cases.push(case("immutable-structured-scalar-scan", 1_000_000, 421));
    cases.push(case(
        "immutable-separate-enrichment-random-lookup",
        1_000_000,
        421,
    ));
    cases.push(case("live-direct-scan", 100_000, 0));
    cases.push(case("immutable-direct-scan", 100_000, 0));
    cases.push(case("live-open", 100_000, 1));
    cases.push(case("live-open", 100_000, 256));
    cases.push(case("snapshot", 100_000, 0));
    cases.push(case("snapshot", 1_000_000, 0));
    cases.push(case("live-validation", 1_000_000, 0));
    cases.push(case("live-membership-validation", 1_000_000, 421));
    cases.push(case("immutable-validation", 1_000_000, 0));
    for size in [10_000, 100_000, 1_000_000] {
        cases.push(case("immutable-feed-random", size, 0));
        cases.push(case("history-project", size, 7));
    }
    cases.push(case("membership-matching-feeds", 100_000, 421));
    cases.push(case("membership-cardinalities", 1_000_000, 64));
    cases.push(case("membership-selected-pair", 1_000_000, 2));
    cases.push(case("membership-all-pairs", 1_000_000, 8));
    cases.push(case("membership-all-pairs", 100_000, 64));
    cases.push(case("direct-provider-join", 1_000_000, 1));
    cases.push(case("membership-provider-join", 1_000_000, 1));
    cases.push(case("algebra-count", 1_000_000, 2));
    cases.push(case("algebra-compare", 1_000_000, 2));
    cases.push(case("algebra-publish-preserve", 1_000_000, 2));
    cases.push(case("algebra-publish-flat", 1_000_000, 2));
    cases.push(case("direct-provider-join", 1_000_000, 421));
    cases.push(case("membership-provider-join", 1_000_000, 421));
    cases.push(case("algebra-count", 1_000_000, 421));
    cases.push(case("algebra-publish-preserve", 1_000_000, 421));
    cases.push(case("update-ipsets-workflow", 1_000_000, 7));
    cases
}

fn ci_cases() -> Vec<Case> {
    vec![
        case("direct-replace", 1_000_000, 0),
        case("last-seen-refresh", 1_000_000, 0),
        case("feed-first-ascending", 1_000_000, 0),
        case("feed-first-random", 1_000_000, 0),
        case("live-direct-lookup", 100_000, 0),
        case("live-direct-random-lookup", 100_000, 0),
        case("live-direct-scan", 100_000, 0),
        case("live-membership-lookup", 100_000, 421),
        case("live-membership-random-lookup", 100_000, 421),
        case("structured-build-random", 1_000_000, 421),
        case("live-structured-scalar-random-lookup", 100_000, 421),
        case("live-structured-threat-random-lookup", 100_000, 421),
        case("live-structured-scalar-scan", 100_000, 421),
        case("live-feed-scan", 100_000, 421),
        case("membership-cardinalities", 1_000_000, 64),
        case("live-validation", 1_000_000, 0),
        case("live-membership-validation", 1_000_000, 421),
        case("update-ipsets-workflow", 1_000_000, 7),
    ]
}

fn case(name: &str, size: usize, auxiliary: usize) -> Case {
    Case {
        name: name.to_owned(),
        size,
        auxiliary,
    }
}
