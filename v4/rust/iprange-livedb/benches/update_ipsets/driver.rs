use std::env;
use std::process::Command;

use crate::scenarios::{self, ScenarioResult};

const HEADER: &str = "scenario,size,aux,work_units,elapsed_ns,units_per_second,alloc_calls,alloc_bytes,rss_before_kib,rss_after_kib,rss_peak_kib,fds_before,fds_after,file_logical_bytes,file_physical_bytes,range_records,feeds,private_artifacts";

#[derive(Clone, Copy)]
struct Case {
    name: &'static str,
    size: usize,
    auxiliary: usize,
}

pub(crate) fn run() -> Result<(), String> {
    let arguments: Vec<String> = env::args()
        .skip(1)
        .filter(|argument| argument != "--bench")
        .collect();
    match arguments.first().map(String::as_str) {
        None | Some("smoke") => run_matrix(smoke_cases()),
        Some("scale") => run_matrix(scale_cases()),
        Some("case") => run_case(&arguments),
        Some("header") => {
            println!("{HEADER}");
            Ok(())
        }
        Some(other) => Err(format!(
            "unknown mode {other:?}; expected smoke, scale, or case"
        )),
    }
}

fn run_matrix(cases: Vec<Case>) -> Result<(), String> {
    println!("{HEADER}");
    let executable = env::current_exe().map_err(|error| error.to_string())?;
    for case in cases {
        let output = Command::new(&executable)
            .arg("case")
            .arg(case.name)
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
        "{},{},{},{},{},{:.3},{},{},{},{},{},{},{},{},{},{},{},{}",
        result.name,
        result.size,
        result.auxiliary,
        result.work_units,
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
        case("nested-overwrite", 1_000, 0),
        case("nested-overwrite", 4_000, 0),
        case("retention-refresh", 1_000, 0),
        case("retention-refresh", 4_000, 0),
        case("feed-replace", 1_000, 8),
        case("feed-replace", 1_000, 64),
        case("membership-import", 1_000, 64),
        case("live-membership-lookup", 4_000, 64),
        case("immutable-membership-lookup", 4_000, 64),
        case("live-feed-scan", 4_000, 64),
        case("immutable-feed-scan", 4_000, 64),
        case("live-direct-lookup", 4_000, 0),
        case("immutable-direct-lookup", 4_000, 0),
        case("live-direct-scan", 4_000, 0),
        case("immutable-direct-scan", 4_000, 0),
        case("live-open", 4_000, 1),
        case("live-open", 4_000, 256),
        case("snapshot", 4_000, 0),
    ]
}

fn scale_cases() -> Vec<Case> {
    let mut cases = Vec::new();
    for size in [10_000, 100_000, 1_000_000] {
        cases.push(case("direct-replace", size, 0));
        cases.push(case("retention-refresh", size, 0));
    }
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
    cases.push(case("membership-import", 10_000, 421));
    cases.push(case("membership-import", 100_000, 421));
    cases.push(case("membership-import", 1_000_000, 421));
    cases.push(case("live-feed-scan", 100_000, 421));
    cases.push(case("immutable-feed-scan", 100_000, 421));
    cases.push(case("live-direct-lookup", 100_000, 0));
    cases.push(case("immutable-direct-lookup", 100_000, 0));
    cases.push(case("live-direct-scan", 100_000, 0));
    cases.push(case("immutable-direct-scan", 100_000, 0));
    cases.push(case("live-open", 100_000, 1));
    cases.push(case("live-open", 100_000, 256));
    cases.push(case("snapshot", 100_000, 0));
    cases.push(case("snapshot", 1_000_000, 0));
    cases
}

const fn case(name: &'static str, size: usize, auxiliary: usize) -> Case {
    Case {
        name,
        size,
        auxiliary,
    }
}
