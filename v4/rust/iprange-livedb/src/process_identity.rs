//! Canonical process-start parsing and conservative death classification.

use crate::sidecar::ActiveSlot;
use crate::sidecar_transition::DeathProof;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ProcessStartParseError {
    MissingCommandEnd,
    MissingField,
    InvalidNumber,
    Overflow,
    Zero,
}

/// Parse Linux `/proc/<pid>/stat` field 22 after the final `)` terminating the
/// command field. This stays allocation-free because command names may contain
/// spaces and `)` bytes.
pub(crate) fn parse_linux_proc_stat_start(bytes: &[u8]) -> Result<u64, ProcessStartParseError> {
    let command_end = bytes
        .iter()
        .rposition(|&byte| byte == b')')
        .ok_or(ProcessStartParseError::MissingCommandEnd)?;
    let tail = &bytes[command_end + 1..];
    let mut fields = tail
        .split(|byte| byte.is_ascii_whitespace())
        .filter(|field| !field.is_empty());
    let start = fields.nth(19).ok_or(ProcessStartParseError::MissingField)?;
    parse_nonzero_u64(start)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PosixProcessObservation {
    Missing,
    Exists { current_start: Option<u64> },
    Uncertain,
}

pub(crate) fn classify_posix_death(
    active: ActiveSlot,
    observation: PosixProcessObservation,
) -> Option<DeathProof> {
    match observation {
        PosixProcessObservation::Missing => Some(DeathProof::PosixMissing {
            process_id: active.process_id,
        }),
        PosixProcessObservation::Exists {
            current_start: Some(current_start),
        } if active.process_start != 0
            && current_start != 0
            && current_start != active.process_start =>
        {
            Some(DeathProof::PosixPidReused {
                process_id: active.process_id,
                current_start,
            })
        }
        PosixProcessObservation::Exists { .. } | PosixProcessObservation::Uncertain => None,
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum WindowsProcessObservation {
    Signaled,
    Running { current_start: Option<u64> },
    Uncertain,
}

pub(crate) fn classify_windows_death(
    active: ActiveSlot,
    observation: WindowsProcessObservation,
) -> Option<DeathProof> {
    match observation {
        WindowsProcessObservation::Signaled => Some(DeathProof::WindowsSignaled {
            process_id: active.process_id,
        }),
        WindowsProcessObservation::Running {
            current_start: Some(current_start),
        } if active.process_start != 0
            && current_start != 0
            && current_start != active.process_start =>
        {
            Some(DeathProof::WindowsPidReused {
                process_id: active.process_id,
                current_start,
            })
        }
        WindowsProcessObservation::Running { .. } | WindowsProcessObservation::Uncertain => None,
    }
}

fn parse_nonzero_u64(bytes: &[u8]) -> Result<u64, ProcessStartParseError> {
    if bytes.is_empty() {
        return Err(ProcessStartParseError::InvalidNumber);
    }
    let mut value = 0u64;
    for &byte in bytes {
        if !byte.is_ascii_digit() {
            return Err(ProcessStartParseError::InvalidNumber);
        }
        value = value
            .checked_mul(10)
            .and_then(|value| value.checked_add(u64::from(byte - b'0')))
            .ok_or(ProcessStartParseError::Overflow)?;
    }
    if value == 0 {
        return Err(ProcessStartParseError::Zero);
    }
    Ok(value)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn active(start: u64) -> ActiveSlot {
        ActiveSlot {
            txn_id: 7,
            process_id: 123,
            process_start: start,
            task_id: 0,
            nonce: [1; 16],
        }
    }

    fn stat(command: &[u8], start: &[u8]) -> std::vec::Vec<u8> {
        let mut bytes = b"123 (".to_vec();
        bytes.extend_from_slice(command);
        bytes.extend_from_slice(b") R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 ");
        bytes.extend_from_slice(start);
        bytes.extend_from_slice(b" 20 21\n");
        bytes
    }

    #[test]
    fn linux_parser_uses_final_parenthesis_and_exact_field_22() {
        assert_eq!(
            parse_linux_proc_stat_start(&stat(b"name with ) embedded", b"424242")),
            Ok(424242)
        );
        assert_eq!(
            parse_linux_proc_stat_start(&stat(b") )", b"18446744073709551615")),
            Ok(u64::MAX)
        );
    }

    #[test]
    fn linux_parser_rejects_missing_invalid_zero_and_overflow() {
        assert_eq!(
            parse_linux_proc_stat_start(b"123 no command end"),
            Err(ProcessStartParseError::MissingCommandEnd)
        );
        assert_eq!(
            parse_linux_proc_stat_start(b"123 (x) R 1 2"),
            Err(ProcessStartParseError::MissingField)
        );
        assert_eq!(
            parse_linux_proc_stat_start(&stat(b"x", b"-1")),
            Err(ProcessStartParseError::InvalidNumber)
        );
        assert_eq!(
            parse_linux_proc_stat_start(&stat(b"x", b"0")),
            Err(ProcessStartParseError::Zero)
        );
        assert_eq!(
            parse_linux_proc_stat_start(&stat(b"x", b"18446744073709551616")),
            Err(ProcessStartParseError::Overflow)
        );
    }

    #[test]
    fn posix_death_requires_esrch_or_two_nonzero_mismatched_tokens() {
        assert_eq!(
            classify_posix_death(active(10), PosixProcessObservation::Missing),
            Some(DeathProof::PosixMissing { process_id: 123 })
        );
        assert_eq!(
            classify_posix_death(
                active(10),
                PosixProcessObservation::Exists {
                    current_start: Some(11),
                },
            ),
            Some(DeathProof::PosixPidReused {
                process_id: 123,
                current_start: 11,
            })
        );
        for observation in [
            PosixProcessObservation::Exists {
                current_start: Some(10),
            },
            PosixProcessObservation::Exists {
                current_start: None,
            },
            PosixProcessObservation::Uncertain,
        ] {
            assert_eq!(classify_posix_death(active(10), observation), None);
        }
        assert_eq!(
            classify_posix_death(
                active(0),
                PosixProcessObservation::Exists {
                    current_start: Some(11),
                },
            ),
            None
        );
    }

    #[test]
    fn windows_death_requires_signaled_handle_or_token_mismatch() {
        assert_eq!(
            classify_windows_death(active(10), WindowsProcessObservation::Signaled),
            Some(DeathProof::WindowsSignaled { process_id: 123 })
        );
        assert_eq!(
            classify_windows_death(
                active(10),
                WindowsProcessObservation::Running {
                    current_start: Some(11),
                },
            ),
            Some(DeathProof::WindowsPidReused {
                process_id: 123,
                current_start: 11,
            })
        );
        assert_eq!(
            classify_windows_death(active(10), WindowsProcessObservation::Uncertain),
            None
        );
    }
}
