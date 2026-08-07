use std::ffi::OsStr;
use std::fs::{File, OpenOptions};
use std::path::{Path, PathBuf};
use std::ptr;
use std::sync::atomic::{AtomicU32, Ordering};
use std::time::{Duration, Instant};

use memmap2::{MmapOptions, MmapRaw};

use crate::error::{Error, Result};
use crate::publication::security::{self, Profile};
use crate::random;

pub(super) const CONTROL_LEN: usize = 1024 * 1024;
pub(super) const ALT_STACK_LEN: usize = 64 * 1024;
pub(super) const OWNED_FAULT_EXIT: i32 = 197;

const MAGIC: &[u8; 8] = b"IPR4WRK\0";
const PROTOCOL: u32 = 1;
const BUILD_ID: &str = env!("IPRANGE_V4_BUILD_ID");
const WAIT_LIMIT: Duration = Duration::from_secs(30);

const MAGIC_AT: usize = 0;
const PROTOCOL_AT: usize = 8;
const STATE_AT: usize = 12;
const BUILD_AT: usize = 16;
const BUILD_LEN: usize = 64;
const NONCE_AT: usize = 80;
const PARENT_PID_AT: usize = 96;
const WORKER_PID_AT: usize = 100;
const MAPPING_GENERATION_AT: usize = 104;
const MAPPING_ROLE_AT: usize = 112;
const PROBE_ARMED_AT: usize = 116;
const HANDLING_AT: usize = 120;
const MAPPING_BASE_AT: usize = 128;
const MAPPING_LEN_AT: usize = 136;
const FAULT_GENERATION_AT: usize = 144;
const FAULT_ROLE_AT: usize = 152;
const FAULT_CODE_AT: usize = 156;
const FAULT_RELATIVE_AT: usize = 160;
const FAULT_ADDRESS_AT: usize = 168;
const FAULT_MARKER_AT: usize = 176;
const OPCODE_AT: usize = 180;
const PAYLOAD_LEN_AT: usize = 184;
const RESPONSE_AT: usize = 188;
const CANCELLED_AT: usize = 192;
const EXTERNAL_POLL_AT: usize = 196;
const GUARD_PENDING_AT: usize = 200;
const SCRATCH_ACTIVE_AT: usize = 204;
const SCRATCH_COUNT_AT: usize = 208;
const RECOVERY_CHECKPOINT_AT: usize = 212;
const SCRATCH_ATTEMPT_AT: usize = 216;
const SCRATCH_DIRECTORY_KIND_AT: usize = 232;
const SCRATCH_DIRECTORY_ID_AT: usize = 240;
const SCRATCH_SECURITY_KIND_AT: usize = 272;
const SCRATCH_SECURITY_AT: usize = 280;
const SCRATCH_ENTRY_AT: usize = 320;
const SCRATCH_ENTRY_LEN: usize = 40;
const SCRATCH_ENTRY_CAPACITY: usize = 2;
const CALLBACK_CHECKPOINT_AT: usize = 400;
const CALLBACK_PAYLOAD_LEN_AT: usize = 404;
const CALLBACK_PAYLOAD_AT: usize = 512;
const CALLBACK_PAYLOAD_CAPACITY: usize = PAYLOAD_AT - CALLBACK_PAYLOAD_AT;
const PAYLOAD_AT: usize = 4096;
const PAYLOAD_CAPACITY: usize = CONTROL_LEN - ALT_STACK_LEN - PAYLOAD_AT;
const FAULT_MARKER: u32 = 0x4255_5346;

const _: () = {
    assert!(SCRATCH_ATTEMPT_AT + 16 <= SCRATCH_DIRECTORY_KIND_AT);
    assert!(SCRATCH_DIRECTORY_KIND_AT + 2 <= SCRATCH_DIRECTORY_ID_AT);
    assert!(SCRATCH_DIRECTORY_ID_AT + 32 <= SCRATCH_SECURITY_KIND_AT);
    assert!(SCRATCH_SECURITY_KIND_AT + 2 <= SCRATCH_SECURITY_AT);
    assert!(SCRATCH_SECURITY_AT + 32 <= SCRATCH_ENTRY_AT);
    assert!(
        SCRATCH_ENTRY_AT + SCRATCH_ENTRY_CAPACITY * SCRATCH_ENTRY_LEN <= CALLBACK_CHECKPOINT_AT
    );
    assert!(CALLBACK_PAYLOAD_LEN_AT + 4 <= CALLBACK_PAYLOAD_AT);
    assert!(CALLBACK_PAYLOAD_AT + CALLBACK_PAYLOAD_CAPACITY <= PAYLOAD_AT);
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(super) enum State {
    Request = 1,
    WorkerReady = 2,
    Running = 3,
    CancelPoll = 4,
    Finding = 5,
    Unknown = 6,
    Complete = 7,
    Fault = 8,
    Failed = 9,
    CleanupRequest = 10,
    CleanupResult = 11,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(super) enum Opcode {
    InspectRecoveryCandidates = 1,
    Validate = 2,
    Recover = 3,
    CleanupRecoveryAttempt = 4,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(super) enum MappingRole {
    Source = 1,
    Scratch = 2,
    Output = 3,
    Coordination = 4,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(super) enum CallbackCheckpoint {
    RecoveryReport = 1,
    ValidationProgress = 2,
}

impl CallbackCheckpoint {
    fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::RecoveryReport),
            2 => Some(Self::ValidationProgress),
            _ => None,
        }
    }
}

impl MappingRole {
    fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::Source),
            2 => Some(Self::Scratch),
            3 => Some(Self::Output),
            4 => Some(Self::Coordination),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) struct FaultRecord {
    pub(super) role: MappingRole,
    pub(super) relative: u64,
    pub(super) mapping_len: u64,
}

#[derive(Clone, Copy)]
pub(super) struct MappingRegistration {
    pub(super) generation: u64,
    pub(super) role: MappingRole,
    pub(super) base: *const u8,
    pub(super) len: usize,
}

#[derive(Clone, Debug)]
pub(super) struct ScratchCheckpoint {
    pub(super) attempt_id: [u8; 16],
    pub(super) directory_identity: crate::validation::LocalFileIdentity,
    pub(super) creation_security: crate::publication::CreationSecurity,
    pub(super) entries: Vec<ScratchCheckpointEntry>,
}

#[derive(Clone, Copy, Debug)]
pub(super) struct ScratchCheckpointEntry {
    pub(super) ordinal: u32,
    pub(super) identity: crate::validation::LocalFileIdentity,
}

pub(super) struct Control {
    _file: File,
    map: MmapRaw,
    path: Option<PathBuf>,
}

impl Control {
    pub(super) fn create_parent() -> Result<Self> {
        let nonce = random::nonzero_128()?;
        let (path, file) = create_file(nonce)?;
        file.set_len(CONTROL_LEN as u64)?;
        let map = map(&file)?;
        let control = Self {
            _file: file,
            map,
            path: Some(path),
        };
        control.clear();
        control.write_bytes(MAGIC_AT, MAGIC);
        control.write_u32(PROTOCOL_AT, PROTOCOL);
        control.write_bytes(BUILD_AT, BUILD_ID.as_bytes());
        control.write_bytes(NONCE_AT, &nonce);
        control.write_u32(PARENT_PID_AT, std::process::id());
        control.set_state(State::Request);
        Ok(control)
    }

    pub(super) fn open_worker(path: &OsStr) -> Result<Self> {
        let path = Path::new(path);
        let file = OpenOptions::new().read(true).write(true).open(path)?;
        if file.metadata()?.len() != CONTROL_LEN as u64 {
            return Err(Error::Corrupt("worker control length is invalid"));
        }
        let map = map(&file)?;
        Ok(Self {
            _file: file,
            map,
            path: None,
        })
    }

    pub(super) fn verify_request(&self) -> Result<()> {
        if !self.bytes_equal(MAGIC_AT, MAGIC)
            || self.read_u32(PROTOCOL_AT) != PROTOCOL
            || BUILD_ID.len() != BUILD_LEN
            || !self.bytes_equal(BUILD_AT, BUILD_ID.as_bytes())
            || self.state() != Some(State::Request)
            || self.read_u32(PARENT_PID_AT) == 0
        {
            return Err(Error::Conflict("worker protocol does not match the SDK"));
        }
        Ok(())
    }

    pub(super) fn path(&self) -> &Path {
        self.path
            .as_deref()
            .expect("only the parent owns the control path")
    }

    #[cfg(unix)]
    pub(super) fn remove_path(&mut self) -> Result<()> {
        let Some(path) = self.path.take() else {
            return Ok(());
        };
        std::fs::remove_file(path)?;
        Ok(())
    }

    pub(super) fn set_worker_pid(&self, pid: u32) {
        self.write_u32(WORKER_PID_AT, pid);
    }

    pub(super) fn worker_pid(&self) -> u32 {
        self.read_u32(WORKER_PID_AT)
    }

    pub(super) fn parent_pid(&self) -> u32 {
        self.read_u32(PARENT_PID_AT)
    }

    pub(super) fn parent_alive(&self) -> bool {
        parent_alive(self.parent_pid())
    }

    pub(super) fn set_opcode(&self, opcode: Opcode) {
        self.write_u32(OPCODE_AT, opcode as u32);
    }

    pub(super) fn opcode(&self) -> Option<Opcode> {
        match self.read_u32(OPCODE_AT) {
            1 => Some(Opcode::InspectRecoveryCandidates),
            2 => Some(Opcode::Validate),
            3 => Some(Opcode::Recover),
            4 => Some(Opcode::CleanupRecoveryAttempt),
            _ => None,
        }
    }

    pub(super) fn set_external_poll(&self, enabled: bool) {
        self.write_u32(EXTERNAL_POLL_AT, u32::from(enabled));
    }

    pub(super) fn external_poll(&self) -> bool {
        self.read_u32(EXTERNAL_POLL_AT) != 0
    }

    pub(super) fn request_cancel(&self) {
        self.atomic_u32(CANCELLED_AT).store(1, Ordering::Release);
    }

    pub(super) fn cancelled(&self) -> bool {
        self.atomic_u32(CANCELLED_AT).load(Ordering::Acquire) != 0
    }

    pub(super) fn set_response(&self, value: u32) {
        self.write_u32(RESPONSE_AT, value);
    }

    pub(super) fn response(&self) -> u32 {
        self.read_u32(RESPONSE_AT)
    }

    pub(super) fn set_guard_pending(&self, pending: bool) {
        self.write_u32(GUARD_PENDING_AT, u32::from(pending));
    }

    pub(super) fn guard_pending(&self) -> bool {
        self.read_u32(GUARD_PENDING_AT) != 0
    }

    pub(super) fn start_scratch_checkpoint(
        &self,
        attempt_id: [u8; 16],
        directory_identity: crate::validation::LocalFileIdentity,
        creation_security: &crate::publication::CreationSecurity,
    ) -> Result<()> {
        if attempt_id == [0; 16] {
            return Err(Error::InvalidArgument("scratch attempt ID is zero"));
        }
        self.atomic_u32(SCRATCH_ACTIVE_AT)
            .store(0, Ordering::Release);
        self.atomic_u32(SCRATCH_COUNT_AT)
            .store(0, Ordering::Release);
        self.write_bytes(SCRATCH_ATTEMPT_AT, &attempt_id);
        self.write_u16(SCRATCH_DIRECTORY_KIND_AT, directory_identity.kind);
        self.write_bytes(SCRATCH_DIRECTORY_ID_AT, &directory_identity.bytes);
        self.write_u16(SCRATCH_SECURITY_KIND_AT, creation_security.kind);
        self.write_bytes(SCRATCH_SECURITY_AT, &creation_security.commitment);
        self.atomic_u32(SCRATCH_ACTIVE_AT)
            .store(1, Ordering::Release);
        Ok(())
    }

    pub(super) fn add_scratch_checkpoint(
        &self,
        ordinal: u32,
        identity: crate::validation::LocalFileIdentity,
    ) -> Result<()> {
        if self.atomic_u32(SCRATCH_ACTIVE_AT).load(Ordering::Acquire) == 0 {
            return Err(Error::Conflict("scratch checkpoint is not active"));
        }
        let count = self.atomic_u32(SCRATCH_COUNT_AT).load(Ordering::Acquire) as usize;
        if count >= SCRATCH_ENTRY_CAPACITY {
            return Err(Error::BudgetExceeded("scratch checkpoint entries"));
        }
        let at = SCRATCH_ENTRY_AT + count * SCRATCH_ENTRY_LEN;
        self.write_u32(at, ordinal);
        self.write_u16(at + 4, identity.kind);
        self.write_bytes(at + 8, &identity.bytes);
        self.atomic_u32(SCRATCH_COUNT_AT)
            .store((count + 1) as u32, Ordering::Release);
        Ok(())
    }

    pub(super) fn scratch_checkpoint(&self) -> Result<Option<ScratchCheckpoint>> {
        if self.atomic_u32(SCRATCH_ACTIVE_AT).load(Ordering::Acquire) == 0 {
            return Ok(None);
        }
        let count = self.atomic_u32(SCRATCH_COUNT_AT).load(Ordering::Acquire) as usize;
        if count > SCRATCH_ENTRY_CAPACITY {
            return Err(Error::Conflict("worker scratch checkpoint is invalid"));
        }
        let attempt_id = self.read_array(SCRATCH_ATTEMPT_AT);
        if attempt_id == [0; 16] {
            return Err(Error::Conflict("worker scratch checkpoint is invalid"));
        }
        let directory_identity = crate::validation::LocalFileIdentity {
            kind: self.read_u16(SCRATCH_DIRECTORY_KIND_AT),
            bytes: self.read_array(SCRATCH_DIRECTORY_ID_AT),
        };
        let creation_security = crate::publication::CreationSecurity {
            kind: self.read_u16(SCRATCH_SECURITY_KIND_AT),
            commitment: self.read_array(SCRATCH_SECURITY_AT),
        };
        if directory_identity.kind != crate::publication::namespace::IDENTITY_KIND
            || crate::publication::namespace::Identity::decode(directory_identity.bytes).is_none()
        {
            return Err(Error::Conflict(
                "worker scratch directory checkpoint is invalid",
            ));
        }
        if creation_security.kind != crate::publication::namespace::CREATION_SECURITY_KIND
            || creation_security.commitment == [0; 32]
        {
            return Err(Error::Conflict(
                "worker scratch security checkpoint is invalid",
            ));
        }
        let mut entries = Vec::with_capacity(count);
        for index in 0..count {
            let at = SCRATCH_ENTRY_AT + index * SCRATCH_ENTRY_LEN;
            let entry = ScratchCheckpointEntry {
                ordinal: self.read_u32(at),
                identity: crate::validation::LocalFileIdentity {
                    kind: self.read_u16(at + 4),
                    bytes: self.read_array(at + 8),
                },
            };
            if entry.identity.kind != crate::publication::namespace::IDENTITY_KIND
                || crate::publication::namespace::Identity::decode(entry.identity.bytes).is_none()
            {
                return Err(Error::Conflict(
                    "worker scratch artifact checkpoint is invalid",
                ));
            }
            if entries.iter().any(|prior: &ScratchCheckpointEntry| {
                prior.ordinal == entry.ordinal || prior.identity == entry.identity
            }) {
                return Err(Error::Conflict(
                    "worker scratch checkpoint contains duplicate authority",
                ));
            }
            entries.push(entry);
        }
        Ok(Some(ScratchCheckpoint {
            attempt_id,
            directory_identity,
            creation_security,
            entries,
        }))
    }

    pub(super) fn begin_recovery_checkpoint(&self) {
        self.atomic_u32(RECOVERY_CHECKPOINT_AT)
            .store(0, Ordering::Release);
    }

    pub(super) fn seal_recovery_checkpoint(&self) {
        self.atomic_u32(RECOVERY_CHECKPOINT_AT)
            .store(1, Ordering::Release);
    }

    pub(super) fn recovery_checkpoint_is_sealed(&self) -> bool {
        self.atomic_u32(RECOVERY_CHECKPOINT_AT)
            .load(Ordering::Acquire)
            == 1
    }

    pub(super) fn begin_callback_checkpoint(&self) {
        self.atomic_u32(CALLBACK_CHECKPOINT_AT)
            .store(0, Ordering::Release);
    }

    pub(super) fn seal_callback_checkpoint(&self, kind: CallbackCheckpoint) {
        self.atomic_u32(CALLBACK_CHECKPOINT_AT)
            .store(kind as u32, Ordering::Release);
    }

    pub(super) fn callback_checkpoint(&self) -> Option<CallbackCheckpoint> {
        CallbackCheckpoint::from_wire(
            self.atomic_u32(CALLBACK_CHECKPOINT_AT)
                .load(Ordering::Acquire),
        )
    }

    pub(super) fn request_external_poll(&self) -> bool {
        if !self.external_poll() {
            return self.cancelled();
        }
        self.set_state(State::CancelPoll);
        while self.state() == Some(State::CancelPoll) {
            if !self.parent_alive() {
                return true;
            }
            std::thread::sleep(Duration::from_millis(1));
        }
        self.response() != 0 || self.cancelled()
    }

    pub(super) fn payload_len(&self) -> Result<usize> {
        let len = self.read_u32(PAYLOAD_LEN_AT) as usize;
        if len <= PAYLOAD_CAPACITY {
            Ok(len)
        } else {
            Err(Error::Corrupt("worker payload length is invalid"))
        }
    }

    pub(super) fn set_payload_len(&self, len: usize) -> Result<()> {
        if len > PAYLOAD_CAPACITY || len > u32::MAX as usize {
            return Err(Error::BudgetExceeded("worker control payload"));
        }
        self.write_u32(PAYLOAD_LEN_AT, len as u32);
        Ok(())
    }

    pub(super) fn payload_byte(&self, at: usize) -> Option<u8> {
        (at < self.payload_len().ok()?)
            .then(|| unsafe { ptr::read_volatile(self.map.as_ptr().add(PAYLOAD_AT + at)) })
    }

    pub(super) fn write_payload(&self, at: usize, bytes: &[u8]) -> Result<()> {
        if !at
            .checked_add(bytes.len())
            .is_some_and(|end| end <= PAYLOAD_CAPACITY)
        {
            return Err(Error::BudgetExceeded("worker control payload"));
        }
        // SAFETY: The checked destination is inside the mapped control payload.
        unsafe {
            ptr::copy_nonoverlapping(
                bytes.as_ptr(),
                self.map.as_mut_ptr().add(PAYLOAD_AT + at),
                bytes.len(),
            )
        };
        Ok(())
    }

    pub(super) fn callback_payload_len(&self) -> Result<usize> {
        let len = self.read_u32(CALLBACK_PAYLOAD_LEN_AT) as usize;
        if len <= CALLBACK_PAYLOAD_CAPACITY {
            Ok(len)
        } else {
            Err(Error::Corrupt(
                "worker callback checkpoint length is invalid",
            ))
        }
    }

    pub(super) fn set_callback_payload_len(&self, len: usize) -> Result<()> {
        if len > CALLBACK_PAYLOAD_CAPACITY || len > u32::MAX as usize {
            return Err(Error::BudgetExceeded("worker callback checkpoint"));
        }
        self.write_u32(CALLBACK_PAYLOAD_LEN_AT, len as u32);
        Ok(())
    }

    pub(super) fn callback_payload_byte(&self, at: usize) -> Option<u8> {
        (at < self.callback_payload_len().ok()?)
            .then(|| unsafe { ptr::read_volatile(self.map.as_ptr().add(CALLBACK_PAYLOAD_AT + at)) })
    }

    pub(super) fn write_callback_payload(&self, at: usize, bytes: &[u8]) -> Result<()> {
        if !at
            .checked_add(bytes.len())
            .is_some_and(|end| end <= CALLBACK_PAYLOAD_CAPACITY)
        {
            return Err(Error::BudgetExceeded("worker callback checkpoint"));
        }
        // SAFETY: The checked destination is inside the mapped callback checkpoint.
        unsafe {
            ptr::copy_nonoverlapping(
                bytes.as_ptr(),
                self.map.as_mut_ptr().add(CALLBACK_PAYLOAD_AT + at),
                bytes.len(),
            )
        };
        Ok(())
    }

    pub(super) fn state(&self) -> Option<State> {
        match self.atomic_u32(STATE_AT).load(Ordering::Acquire) {
            1 => Some(State::Request),
            2 => Some(State::WorkerReady),
            3 => Some(State::Running),
            4 => Some(State::CancelPoll),
            5 => Some(State::Finding),
            6 => Some(State::Unknown),
            7 => Some(State::Complete),
            8 => Some(State::Fault),
            9 => Some(State::Failed),
            10 => Some(State::CleanupRequest),
            11 => Some(State::CleanupResult),
            _ => None,
        }
    }

    pub(super) fn set_state(&self, state: State) {
        self.atomic_u32(STATE_AT)
            .store(state as u32, Ordering::Release);
    }

    pub(super) fn wait_for(&self, wanted: State) -> Result<()> {
        let deadline = Instant::now() + WAIT_LIMIT;
        while Instant::now() < deadline {
            if self.state() == Some(wanted) {
                return Ok(());
            }
            if !self.parent_alive() {
                return Err(Error::Conflict("SDK worker parent exited"));
            }
            std::thread::sleep(Duration::from_millis(1));
        }
        Err(Error::Conflict("worker protocol timed out"))
    }

    pub(super) fn arm(
        &self,
        generation: u64,
        role: MappingRole,
        base: *const u8,
        len: usize,
    ) -> Result<()> {
        if generation == 0 || base.is_null() || len == 0 {
            return Err(Error::InvalidArgument("worker mapping probe is empty"));
        }
        self.atomic_u32(HANDLING_AT).store(0, Ordering::Release);
        self.write_u64(MAPPING_GENERATION_AT, generation);
        self.write_u32(MAPPING_ROLE_AT, role as u32);
        self.write_u64(MAPPING_BASE_AT, base as usize as u64);
        self.write_u64(MAPPING_LEN_AT, len as u64);
        self.atomic_u32(PROBE_ARMED_AT).store(1, Ordering::Release);
        Ok(())
    }

    pub(super) fn fault_record(&self) -> Result<FaultRecord> {
        let fields = Self::fault_fields();
        if self.state() != Some(State::Fault)
            || self.read_u32(fields.fault_marker) != fields.marker
            || self.atomic_u32(fields.handling).load(Ordering::Acquire) != 1
        {
            return Err(Error::Conflict("worker fault record is incomplete"));
        }
        let generation = self.read_u64(fields.generation);
        let fault_generation = self.read_u64(fields.fault_generation);
        let role = MappingRole::from_wire(self.read_u32(fields.role))
            .ok_or(Error::Conflict("worker mapping role is invalid"))?;
        let fault_role = MappingRole::from_wire(self.read_u32(fields.fault_role))
            .ok_or(Error::Conflict("worker fault role is invalid"))?;
        let base = self.read_u64(fields.base);
        let mapping_len = self.read_u64(fields.len);
        let fault_code = self.read_i32(fields.fault_code);
        let relative = self.read_u64(fields.fault_relative);
        let address = self.read_u64(fields.fault_address);
        if generation == 0
            || generation != fault_generation
            || role != fault_role
            || !fault_code_valid(fault_code)
            || mapping_len == 0
            || relative >= mapping_len
            || base.checked_add(relative) != Some(address)
        {
            return Err(Error::Conflict("worker fault ownership is inconsistent"));
        }
        Ok(FaultRecord {
            role,
            relative,
            mapping_len,
        })
    }

    pub(super) fn registration(&self) -> Result<Option<MappingRegistration>> {
        if self.atomic_u32(PROBE_ARMED_AT).load(Ordering::Acquire) == 0 {
            return Ok(None);
        }
        let generation = self.read_u64(MAPPING_GENERATION_AT);
        let role = MappingRole::from_wire(self.read_u32(MAPPING_ROLE_AT))
            .ok_or(Error::Conflict("worker mapping role is invalid"))?;
        let base = self.read_u64(MAPPING_BASE_AT);
        let len = self.read_u64(MAPPING_LEN_AT);
        if generation == 0 || base == 0 || len == 0 || len > usize::MAX as u64 {
            return Err(Error::Conflict("worker mapping registration is invalid"));
        }
        Ok(Some(MappingRegistration {
            generation,
            role,
            base: base as usize as *const u8,
            len: len as usize,
        }))
    }

    pub(super) fn disarm(&self) {
        self.atomic_u32(PROBE_ARMED_AT).store(0, Ordering::Release);
    }

    #[cfg(unix)]
    pub(super) fn alt_stack(&self) -> (*mut u8, usize) {
        let at = CONTROL_LEN - ALT_STACK_LEN;
        // SAFETY: The control mapping is live and `at` is within it.
        (unsafe { self.map.as_mut_ptr().add(at) }, ALT_STACK_LEN)
    }

    pub(super) fn base(&self) -> *mut u8 {
        self.map.as_mut_ptr()
    }

    pub(super) fn fault_fields() -> FaultFields {
        FaultFields {
            state: STATE_AT,
            armed: PROBE_ARMED_AT,
            handling: HANDLING_AT,
            generation: MAPPING_GENERATION_AT,
            role: MAPPING_ROLE_AT,
            base: MAPPING_BASE_AT,
            len: MAPPING_LEN_AT,
            fault_generation: FAULT_GENERATION_AT,
            fault_role: FAULT_ROLE_AT,
            fault_code: FAULT_CODE_AT,
            fault_relative: FAULT_RELATIVE_AT,
            fault_address: FAULT_ADDRESS_AT,
            fault_marker: FAULT_MARKER_AT,
            marker: FAULT_MARKER,
        }
    }

    fn clear(&self) {
        // SAFETY: This process exclusively initializes the new mapped control file.
        unsafe { ptr::write_bytes(self.map.as_mut_ptr(), 0, CONTROL_LEN) };
    }

    fn bytes_equal(&self, at: usize, expected: &[u8]) -> bool {
        expected.iter().enumerate().all(|(index, expected)| {
            // SAFETY: Every fixed protocol field is inside the control mapping.
            unsafe { ptr::read_volatile(self.map.as_ptr().add(at + index)) == *expected }
        })
    }

    fn write_bytes(&self, at: usize, bytes: &[u8]) {
        debug_assert!(at + bytes.len() <= CONTROL_LEN);
        // SAFETY: The fixed protocol field is inside the writable control map.
        unsafe {
            ptr::copy_nonoverlapping(bytes.as_ptr(), self.map.as_mut_ptr().add(at), bytes.len())
        };
    }

    fn read_u32(&self, at: usize) -> u32 {
        // SAFETY: Protocol integer offsets are aligned and in bounds.
        unsafe { ptr::read_volatile(self.map.as_ptr().add(at).cast::<u32>()) }
    }

    fn read_i32(&self, at: usize) -> i32 {
        // SAFETY: Protocol integer offsets are aligned and in bounds.
        unsafe { ptr::read_volatile(self.map.as_ptr().add(at).cast::<i32>()) }
    }

    fn read_u16(&self, at: usize) -> u16 {
        unsafe { ptr::read_volatile(self.map.as_ptr().add(at).cast::<u16>()) }
    }

    fn read_array<const N: usize>(&self, at: usize) -> [u8; N] {
        let mut value = [0; N];
        for (index, byte) in value.iter_mut().enumerate() {
            *byte = unsafe { ptr::read_volatile(self.map.as_ptr().add(at + index)) };
        }
        value
    }

    fn read_u64(&self, at: usize) -> u64 {
        // SAFETY: Protocol integer offsets are aligned and in bounds.
        unsafe { ptr::read_volatile(self.map.as_ptr().add(at).cast::<u64>()) }
    }

    fn write_u32(&self, at: usize, value: u32) {
        // SAFETY: Protocol integer offsets are aligned and in bounds.
        unsafe { ptr::write_volatile(self.map.as_mut_ptr().add(at).cast::<u32>(), value) };
    }

    fn write_u16(&self, at: usize, value: u16) {
        unsafe { ptr::write_volatile(self.map.as_mut_ptr().add(at).cast::<u16>(), value) };
    }

    fn write_u64(&self, at: usize, value: u64) {
        // SAFETY: Protocol integer offsets are aligned and in bounds.
        unsafe { ptr::write_volatile(self.map.as_mut_ptr().add(at).cast::<u64>(), value) };
    }

    fn atomic_u32(&self, at: usize) -> &AtomicU32 {
        // SAFETY: The field is aligned, initialized, and lives as long as `self`.
        unsafe { &*self.map.as_ptr().add(at).cast::<AtomicU32>() }
    }
}

impl Drop for Control {
    fn drop(&mut self) {
        if let Some(path) = self.path.take() {
            let _ = std::fs::remove_file(path);
        }
    }
}

#[cfg(unix)]
const fn fault_code_valid(code: i32) -> bool {
    code > 0
}

#[cfg(windows)]
const fn fault_code_valid(code: i32) -> bool {
    code != 0
}

#[derive(Clone, Copy)]
pub(super) struct FaultFields {
    pub(super) state: usize,
    pub(super) armed: usize,
    pub(super) handling: usize,
    pub(super) generation: usize,
    pub(super) role: usize,
    pub(super) base: usize,
    pub(super) len: usize,
    pub(super) fault_generation: usize,
    pub(super) fault_role: usize,
    pub(super) fault_code: usize,
    pub(super) fault_relative: usize,
    pub(super) fault_address: usize,
    pub(super) fault_marker: usize,
    pub(super) marker: u32,
}

fn map(file: &File) -> Result<MmapRaw> {
    let mut options = MmapOptions::new();
    options.len(CONTROL_LEN);
    Ok(options.map_raw(file)?)
}

#[cfg(unix)]
fn create_file(nonce: [u8; 16]) -> Result<(PathBuf, File)> {
    use std::os::unix::fs::OpenOptionsExt;

    let path = control_path(nonce);
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .mode(security::CREATOR_MODE)
        .open(&path)?;
    let profile = Profile::capture().map_err(namespace_error)?;
    security::secure_creator_only(&file, &profile).map_err(namespace_error)?;
    Ok((path, file))
}

#[cfg(windows)]
fn create_file(nonce: [u8; 16]) -> Result<(PathBuf, File)> {
    let path = control_path(nonce);
    let profile = Profile::capture().map_err(namespace_error)?;
    let file = security::create_private(&path, &profile, false).map_err(namespace_error)?;
    Ok((path, file))
}

fn control_path(nonce: [u8; 16]) -> PathBuf {
    let mut name = String::from(".iprange-v4-worker-");
    for byte in nonce {
        use std::fmt::Write as _;
        let _ = write!(name, "{byte:02x}");
    }
    name.push_str(".ctl");
    std::env::temp_dir().join(name)
}

fn namespace_error(_error: crate::publication::namespace::NamespaceError) -> Error {
    Error::Conflict("worker control access policy could not be established")
}

#[cfg(unix)]
fn parent_alive(expected: u32) -> bool {
    expected != 0 && unsafe { libc::getppid() } as u32 == expected
}

#[cfg(windows)]
fn parent_alive(expected: u32) -> bool {
    use windows_sys::Win32::Foundation::{CloseHandle, WAIT_TIMEOUT};
    use windows_sys::Win32::System::Threading::{
        OpenProcess, WaitForSingleObject, PROCESS_SYNCHRONIZE,
    };

    if expected == 0 {
        return false;
    }
    let handle = unsafe { OpenProcess(PROCESS_SYNCHRONIZE, 0, expected) };
    if handle.is_null() {
        return false;
    }
    let state = unsafe { WaitForSingleObject(handle, 0) };
    let _ = unsafe { CloseHandle(handle) };
    state == WAIT_TIMEOUT
}
