//! Repeated benchmark sampling and accepted-baseline comparison.

use std::env;
use std::fs;
use std::path::Path;
use std::process::Command;

use crate::driver::Case;

const FIXTURE_ID: &str = "iprange-v4-update-ipsets-v1";
const BASELINE_ID: &str = "rust-v4-local-20260811";
const BASELINE: &str = include_str!("accepted-baseline.csv");
const HEADER: &str = "scenario,size,aux,samples,min_ns,p50_ns,p90_ns,max_ns,median_units_per_second,alloc_calls,alloc_bytes,max_rss_peak_kib,max_fds_after,file_logical_bytes,range_records,feeds,accepted_median_ns,ci_limit_ns,ratio,status";

#[derive(Clone)]
struct Sample {
    scenario: String,
    size: usize,
    auxiliary: usize,
    work_units: u64,
    emitted_units: u64,
    elapsed_ns: u64,
    allocation_calls: u64,
    allocation_bytes: u64,
    rss_peak_kib: Option<u64>,
    fds_after: Option<u64>,
    file_logical_bytes: u64,
    range_records: u64,
    feeds: u64,
    private_artifacts: u64,
}

#[derive(Clone, Copy)]
struct Accepted {
    median_ns: u64,
    ci_limit_ns: u64,
}

pub(crate) fn run_repeated(
    cases: Vec<Case>,
    warmups: usize,
    samples: usize,
    enforce: bool,
) -> Result<(), String> {
    if samples == 0 {
        return Err("sample count must be positive".to_owned());
    }
    let executable = env::current_exe().map_err(|error| error.to_string())?;
    metadata(warmups, samples);
    println!("{HEADER}");
    let mut failed = Vec::new();
    for case in cases {
        for _ in 0..warmups {
            child_sample(&executable, &case)?;
        }
        let mut observed = Vec::with_capacity(samples);
        for _ in 0..samples {
            observed.push(child_sample(&executable, &case)?);
        }
        require_same_result(&observed)?;
        let accepted = accepted(&observed[0])?;
        let summary = summarize(&observed, accepted);
        println!("{}", summary.line);
        if enforce && summary.over_limit {
            failed.push(format!(
                "{} size={} aux={} median={}ns limit={}ns",
                case.name,
                case.size,
                case.auxiliary,
                summary.median_ns,
                accepted.map(|value| value.ci_limit_ns).unwrap_or_default()
            ));
        }
    }
    if failed.is_empty() {
        Ok(())
    } else {
        Err(format!(
            "performance disaster gate failed: {}",
            failed.join("; ")
        ))
    }
}

struct Summary {
    line: String,
    median_ns: u64,
    over_limit: bool,
}

fn summarize(samples: &[Sample], accepted: Option<Accepted>) -> Summary {
    let mut elapsed: Vec<u64> = samples.iter().map(|sample| sample.elapsed_ns).collect();
    elapsed.sort_unstable();
    let first = &samples[0];
    let median_ns = percentile(&elapsed, 50);
    let accepted_ns = accepted.map(|value| value.median_ns);
    let limit_ns = accepted.map(|value| value.ci_limit_ns);
    let ratio = accepted_ns.map(|baseline| median_ns as f64 / baseline as f64);
    let over_limit = match limit_ns {
        Some(limit) => median_ns > limit,
        None => true,
    };
    let status = match (accepted, over_limit) {
        (None, _) => "untracked",
        (Some(_), true) => "over-limit",
        (Some(_), false) => "within-limit",
    };
    let rate = if median_ns == 0 {
        0.0
    } else {
        first.work_units as f64 * 1_000_000_000.0 / median_ns as f64
    };
    let max_allocations = samples
        .iter()
        .map(|sample| sample.allocation_calls)
        .max()
        .unwrap_or_default();
    let max_allocation_bytes = samples
        .iter()
        .map(|sample| sample.allocation_bytes)
        .max()
        .unwrap_or_default();
    let max_rss = samples
        .iter()
        .filter_map(|sample| sample.rss_peak_kib)
        .max();
    let max_fds = samples.iter().filter_map(|sample| sample.fds_after).max();
    let line = format!(
        "{},{},{},{},{},{},{},{},{:.3},{},{},{},{},{},{},{},{},{},{},{}",
        first.scenario,
        first.size,
        first.auxiliary,
        samples.len(),
        elapsed[0],
        median_ns,
        percentile(&elapsed, 90),
        elapsed[elapsed.len() - 1],
        rate,
        max_allocations,
        max_allocation_bytes,
        optional(max_rss),
        optional(max_fds),
        first.file_logical_bytes,
        first.range_records,
        first.feeds,
        optional(accepted_ns),
        optional(limit_ns),
        ratio.map_or_else(String::new, |value| format!("{value:.3}")),
        status,
    );
    Summary {
        line,
        median_ns,
        over_limit,
    }
}

fn percentile(sorted: &[u64], percentile: usize) -> u64 {
    let rank = (sorted.len() * percentile).div_ceil(100);
    sorted[rank.saturating_sub(1).min(sorted.len() - 1)]
}

fn child_sample(executable: &Path, case: &Case) -> Result<Sample, String> {
    let output = Command::new(executable)
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
    let stdout = String::from_utf8(output.stdout)
        .map_err(|_| format!("{} emitted non-UTF-8 output", case.name))?;
    let mut lines = stdout.lines().filter(|line| !line.is_empty());
    let line = lines
        .next()
        .ok_or_else(|| format!("{} emitted no result", case.name))?;
    if lines.next().is_some() {
        return Err(format!("{} emitted more than one result", case.name));
    }
    Sample::parse(line, case)
}

impl Sample {
    fn parse(line: &str, case: &Case) -> Result<Self, String> {
        let fields: Vec<&str> = line.split(',').collect();
        if fields.len() != 19 {
            return Err(format!(
                "{} emitted {} fields instead of 19",
                case.name,
                fields.len()
            ));
        }
        let sample = Self {
            scenario: fields[0].to_owned(),
            size: parse(fields[1], "size")?,
            auxiliary: parse(fields[2], "auxiliary")?,
            work_units: parse(fields[3], "work units")?,
            emitted_units: parse(fields[4], "emitted units")?,
            elapsed_ns: parse(fields[5], "elapsed time")?,
            allocation_calls: parse(fields[7], "allocation calls")?,
            allocation_bytes: parse(fields[8], "allocation bytes")?,
            rss_peak_kib: parse_optional(fields[11], "peak RSS")?,
            fds_after: parse_optional(fields[13], "file descriptors")?,
            file_logical_bytes: parse(fields[14], "logical bytes")?,
            range_records: parse(fields[16], "range records")?,
            feeds: parse(fields[17], "feeds")?,
            private_artifacts: parse(fields[18], "private artifacts")?,
        };
        if sample.scenario != case.name.as_str()
            || sample.size != case.size
            || sample.auxiliary != case.auxiliary
        {
            return Err(format!("{} result identity disagrees", case.name));
        }
        Ok(sample)
    }
}

fn require_same_result(samples: &[Sample]) -> Result<(), String> {
    let first = &samples[0];
    for sample in &samples[1..] {
        if sample.work_units != first.work_units
            || sample.emitted_units != first.emitted_units
            || sample.auxiliary != first.auxiliary
            || sample.file_logical_bytes != first.file_logical_bytes
            || sample.range_records != first.range_records
            || sample.feeds != first.feeds
            || sample.private_artifacts != first.private_artifacts
        {
            return Err(format!(
                "{} repeated samples produced different semantic results",
                first.scenario
            ));
        }
    }
    Ok(())
}

fn accepted(sample: &Sample) -> Result<Option<Accepted>, String> {
    for (line_number, line) in BASELINE.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') || line.starts_with("scenario,") {
            continue;
        }
        let fields: Vec<&str> = line.split(',').collect();
        if fields.len() != 5 {
            return Err(format!(
                "accepted baseline line {} has {} fields instead of 5",
                line_number + 1,
                fields.len()
            ));
        }
        if fields[0] == sample.scenario
            && parse::<usize>(fields[1], "baseline size")? == sample.size
            && parse::<usize>(fields[2], "baseline auxiliary")? == sample.auxiliary
        {
            return Ok(Some(Accepted {
                median_ns: parse(fields[3], "accepted median")?,
                ci_limit_ns: parse(fields[4], "CI limit")?,
            }));
        }
    }
    Ok(None)
}

fn metadata(warmups: usize, samples: usize) {
    println!("# benchmark=update_ipsets");
    println!("# fixture={FIXTURE_ID}");
    println!("# baseline={BASELINE_ID}");
    println!("# os={}", env::consts::OS);
    println!("# arch={}", env::consts::ARCH);
    println!(
        "# profile={}",
        if cfg!(debug_assertions) {
            "debug"
        } else {
            "optimized"
        }
    );
    println!("# rustc={}", sanitize(&rustc_version()));
    println!("# cpu={}", sanitize(&cpu_model()));
    println!("# warmups={warmups}");
    println!("# samples={samples}");
}

fn rustc_version() -> String {
    Command::new("rustc")
        .arg("--version")
        .output()
        .ok()
        .filter(|output| output.status.success())
        .and_then(|output| String::from_utf8(output.stdout).ok())
        .map_or_else(|| "unknown".to_owned(), |value| value.trim().to_owned())
}

fn cpu_model() -> String {
    if let Ok(value) = env::var("PROCESSOR_IDENTIFIER") {
        return value;
    }
    if let Ok(cpuinfo) = fs::read_to_string("/proc/cpuinfo") {
        if let Some(value) = cpuinfo.lines().find_map(|line| {
            let (key, value) = line.split_once(':')?;
            (key.trim() == "model name").then(|| value.trim().to_owned())
        }) {
            return value;
        }
    }
    for key in ["machdep.cpu.brand_string", "hw.model"] {
        if let Ok(output) = Command::new("sysctl").args(["-n", key]).output() {
            if output.status.success() {
                if let Ok(value) = String::from_utf8(output.stdout) {
                    let value = value.trim();
                    if !value.is_empty() {
                        return value.to_owned();
                    }
                }
            }
        }
    }
    "unknown".to_owned()
}

fn sanitize(value: &str) -> String {
    value
        .chars()
        .map(|character| match character {
            ',' | '\n' | '\r' => ' ',
            other => other,
        })
        .collect()
}

fn parse<T: std::str::FromStr>(value: &str, label: &str) -> Result<T, String> {
    value
        .parse()
        .map_err(|_| format!("invalid {label} {value:?}"))
}

fn parse_optional<T: std::str::FromStr>(value: &str, label: &str) -> Result<Option<T>, String> {
    if value.is_empty() {
        Ok(None)
    } else {
        parse(value, label).map(Some)
    }
}

fn optional<T: ToString>(value: Option<T>) -> String {
    value.map_or_else(String::new, |value| value.to_string())
}
