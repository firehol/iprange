use std::ffi::{c_int, c_void};
use std::mem::{self, MaybeUninit};
use std::ptr;
use std::sync::atomic::{AtomicPtr, AtomicU32, Ordering};

use crate::error::{Error, Result};

use super::control::{Control, State, OWNED_FAULT_EXIT};

const UNOWNED_REDISPATCH_FAILED: c_int = 198;

static ACTIVE_CONTROL: AtomicPtr<u8> = AtomicPtr::new(ptr::null_mut());
static mut PREVIOUS_ACTION: MaybeUninit<libc::sigaction> = MaybeUninit::uninit();

pub(super) struct Handler<'a> {
    control: &'a Control,
    previous_action: libc::sigaction,
    previous_stack: libc::stack_t,
}

impl<'a> Handler<'a> {
    pub(super) fn install(control: &'a Control) -> Result<Self> {
        if !ACTIVE_CONTROL.load(Ordering::Acquire).is_null() {
            return Err(Error::Conflict(
                "SIGBUS worker handler is already installed",
            ));
        }
        let (stack, stack_len) = control.alt_stack();
        let mut previous_stack = unsafe { mem::zeroed::<libc::stack_t>() };
        let selected_stack = libc::stack_t {
            ss_sp: stack.cast(),
            ss_flags: 0,
            ss_size: stack_len,
        };
        // SAFETY: Both stack descriptors are valid and the mapped alternate
        // stack outlives the installed handler.
        if unsafe { libc::sigaltstack(&selected_stack, &mut previous_stack) } != 0 {
            return Err(std::io::Error::last_os_error().into());
        }

        let mut previous_action = unsafe { mem::zeroed::<libc::sigaction>() };
        // Publish the predecessor before our handler can run. This closes the
        // installation window where an unrelated signal could otherwise chain
        // through uninitialized storage.
        if unsafe { libc::sigaction(libc::SIGBUS, ptr::null(), &mut previous_action) } != 0 {
            restore_stack(&previous_stack);
            return Err(std::io::Error::last_os_error().into());
        }
        unsafe { ptr::write(ptr::addr_of_mut!(PREVIOUS_ACTION).cast(), previous_action) };
        let mut selected_action = unsafe { mem::zeroed::<libc::sigaction>() };
        selected_action.sa_sigaction = signal_handler as *const () as usize;
        selected_action.sa_flags = (libc::SA_SIGINFO | libc::SA_ONSTACK) as _;
        // SAFETY: The action is initialized and SIGBUS is a valid signal.
        unsafe { libc::sigemptyset(&mut selected_action.sa_mask) };
        // SAFETY: The worker is single-threaded and the predecessor was captured above.
        if unsafe { libc::sigaction(libc::SIGBUS, &selected_action, ptr::null_mut()) } != 0 {
            restore_stack(&previous_stack);
            return Err(std::io::Error::last_os_error().into());
        }
        if ACTIVE_CONTROL
            .compare_exchange(
                ptr::null_mut(),
                control.base(),
                Ordering::Release,
                Ordering::Relaxed,
            )
            .is_err()
        {
            restore_action(&previous_action);
            restore_stack(&previous_stack);
            return Err(Error::Conflict(
                "SIGBUS worker handler is already installed",
            ));
        }

        let handler = Self {
            control,
            previous_action,
            previous_stack,
        };
        if let Err(cause) = handler.verify_owned() {
            drop(handler);
            return Err(cause);
        }
        Ok(handler)
    }

    pub(super) fn verify_owned(&self) -> Result<()> {
        verify_owned(self.control)
    }
}

pub(super) fn verify_owned(control: &Control) -> Result<()> {
    let current = current_action()?;
    let required = (libc::SA_SIGINFO | libc::SA_ONSTACK) as libc::c_int;
    let current_stack = current_stack()?;
    let (required_stack, required_stack_len) = control.alt_stack();
    if current.sa_sigaction != signal_handler as *const () as usize
        || current.sa_flags as libc::c_int & required != required
        || current.sa_flags as libc::c_int & (libc::SA_NODEFER | libc::SA_RESETHAND) != 0
        || current_stack.ss_flags & libc::SS_DISABLE != 0
        || current_stack.ss_sp != required_stack.cast()
        || current_stack.ss_size != required_stack_len
        || ACTIVE_CONTROL.load(Ordering::Acquire) != control.base()
    {
        return Err(Error::Conflict("SIGBUS worker handler ownership was lost"));
    }
    Ok(())
}

impl Drop for Handler<'_> {
    fn drop(&mut self) {
        self.control.disarm();
        if ACTIVE_CONTROL
            .compare_exchange(
                self.control.base(),
                ptr::null_mut(),
                Ordering::AcqRel,
                Ordering::Relaxed,
            )
            .is_ok()
            && current_action()
                .is_ok_and(|action| action.sa_sigaction == signal_handler as *const () as usize)
        {
            restore_action(&self.previous_action);
        }
        let (stack, stack_len) = self.control.alt_stack();
        if current_stack().is_ok_and(|current| {
            current.ss_flags & libc::SS_DISABLE == 0
                && current.ss_sp == stack.cast()
                && current.ss_size == stack_len
        }) {
            restore_stack(&self.previous_stack);
        }
    }
}

fn current_action() -> Result<libc::sigaction> {
    let mut current = unsafe { mem::zeroed::<libc::sigaction>() };
    // SAFETY: A null action queries the current disposition.
    if unsafe { libc::sigaction(libc::SIGBUS, ptr::null(), &mut current) } != 0 {
        return Err(std::io::Error::last_os_error().into());
    }
    Ok(current)
}

fn current_stack() -> Result<libc::stack_t> {
    let mut current = unsafe { mem::zeroed::<libc::stack_t>() };
    // SAFETY: A null stack queries the current alternate stack.
    if unsafe { libc::sigaltstack(ptr::null(), &mut current) } != 0 {
        return Err(std::io::Error::last_os_error().into());
    }
    Ok(current)
}

extern "C" fn signal_handler(signal: c_int, info: *mut libc::siginfo_t, context: *mut c_void) {
    let control = ACTIVE_CONTROL.load(Ordering::Acquire);
    if control.is_null() || !owned_fault(control, signal, info) {
        chain(signal, info, context);
        return;
    }
    // SAFETY: `owned_fault` recorded fixed facts in the shared control map.
    unsafe { libc::_exit(OWNED_FAULT_EXIT) }
}

fn owned_fault(control: *mut u8, signal: c_int, info: *mut libc::siginfo_t) -> bool {
    if signal != libc::SIGBUS || info.is_null() {
        return false;
    }
    let code = unsafe { (*info).si_code };
    if !kernel_bus_code(code) {
        return false;
    }
    let fields = Control::fault_fields();
    if atomic_u32(control, fields.armed).load(Ordering::Acquire) != 1 {
        return false;
    }
    let generation = read_u64(control, fields.generation);
    let role = read_u32(control, fields.role);
    let base = read_u64(control, fields.base);
    let len = read_u64(control, fields.len);
    let address = unsafe { (*info).si_addr() as usize as u64 };
    let Some(relative) = address.checked_sub(base) else {
        return false;
    };
    if generation == 0 || !(1..=4).contains(&role) || len == 0 || relative >= len {
        return false;
    }

    if atomic_u32(control, fields.handling)
        .compare_exchange(0, 1, Ordering::AcqRel, Ordering::Relaxed)
        .is_err()
    {
        return false;
    }
    write_u64(control, fields.fault_generation, generation);
    write_u32(control, fields.fault_role, role);
    write_i32(control, fields.fault_code, code);
    write_u64(control, fields.fault_relative, relative);
    write_u64(control, fields.fault_address, address);
    write_u32(control, fields.fault_marker, fields.marker);
    atomic_u32(control, fields.state).store(State::Fault as u32, Ordering::Release);
    true
}

fn chain(signal: c_int, info: *mut libc::siginfo_t, context: *mut c_void) {
    let previous = previous_action();
    ACTIVE_CONTROL.store(ptr::null_mut(), Ordering::Release);
    let disposition = previous.sa_sigaction;
    if disposition == libc::SIG_DFL {
        if unsafe { libc::sigaction(signal, previous, ptr::null_mut()) } != 0 {
            unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
        }
        if !synchronous_bus_fault(info) {
            redispatch_default(signal);
        }
        return;
    }
    if disposition == libc::SIG_IGN {
        if unsafe { libc::sigaction(signal, previous, ptr::null_mut()) } != 0 {
            unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
        }
        return;
    }

    let reset = previous.sa_flags as libc::c_int & libc::SA_RESETHAND != 0;
    if reset {
        let mut default_action = unsafe { mem::zeroed::<libc::sigaction>() };
        default_action.sa_sigaction = libc::SIG_DFL;
        unsafe { libc::sigemptyset(&mut default_action.sa_mask) };
        if unsafe { libc::sigaction(signal, &default_action, ptr::null_mut()) } != 0 {
            unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
        }
        if !apply_mask(previous, signal) {
            unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
        }
        call_action(previous, signal, info, context);
        return;
    }
    if unsafe { libc::sigaction(signal, previous, ptr::null_mut()) } != 0 {
        unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
    }
    if !apply_mask(previous, signal) {
        unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
    }

    call_action(previous, signal, info, context);
}

fn call_action(
    previous: &libc::sigaction,
    signal: c_int,
    info: *mut libc::siginfo_t,
    context: *mut c_void,
) {
    if previous.sa_flags as libc::c_int & libc::SA_SIGINFO != 0 {
        // SAFETY: SA_SIGINFO selects the three-argument handler ABI.
        let handler: extern "C" fn(c_int, *mut libc::siginfo_t, *mut c_void) =
            unsafe { mem::transmute(previous.sa_sigaction) };
        handler(signal, info, context);
    } else {
        // SAFETY: The saved action uses the one-argument handler ABI.
        let handler: extern "C" fn(c_int) = unsafe { mem::transmute(previous.sa_sigaction) };
        handler(signal);
    }
}

fn synchronous_bus_fault(info: *mut libc::siginfo_t) -> bool {
    !info.is_null()
        && !unsafe { (*info).si_addr() }.is_null()
        && kernel_bus_code(unsafe { (*info).si_code })
}

fn redispatch_default(signal: c_int) -> ! {
    if unsafe { libc::kill(libc::getpid(), signal) } != 0 {
        unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
    }
    let mut selected = unsafe { mem::zeroed::<libc::sigset_t>() };
    if unsafe { libc::sigprocmask(libc::SIG_SETMASK, ptr::null(), &mut selected) } != 0
        || unsafe { libc::sigdelset(&mut selected, signal) } != 0
    {
        unsafe { libc::_exit(UNOWNED_REDISPATCH_FAILED) };
    }
    loop {
        unsafe { libc::sigsuspend(&selected) };
    }
}

fn kernel_bus_code(code: c_int) -> bool {
    code == libc::BUS_ADRALN
        || code == libc::BUS_ADRERR
        || code == libc::BUS_OBJERR
        || linux_machine_check(code)
}

#[cfg(target_os = "linux")]
fn linux_machine_check(code: c_int) -> bool {
    code == libc::BUS_MCEERR_AR || code == libc::BUS_MCEERR_AO
}

#[cfg(not(target_os = "linux"))]
const fn linux_machine_check(_code: c_int) -> bool {
    false
}

fn apply_mask(previous: &libc::sigaction, signal: c_int) -> bool {
    let mut selected = unsafe { mem::zeroed::<libc::sigset_t>() };
    if unsafe { libc::sigprocmask(libc::SIG_SETMASK, ptr::null(), &mut selected) } != 0 {
        return false;
    }
    let bits = mem::size_of::<libc::sigset_t>() * 8;
    for candidate in 1..bits.min(c_int::MAX as usize) {
        let candidate = candidate as c_int;
        if unsafe { libc::sigismember(&previous.sa_mask, candidate) } == 1
            && unsafe { libc::sigaddset(&mut selected, candidate) } != 0
        {
            return false;
        }
    }
    let nodefer = previous.sa_flags as libc::c_int & libc::SA_NODEFER != 0;
    let changed = if nodefer {
        unsafe { libc::sigdelset(&mut selected, signal) }
    } else {
        unsafe { libc::sigaddset(&mut selected, signal) }
    };
    changed == 0 && unsafe { libc::sigprocmask(libc::SIG_SETMASK, &selected, ptr::null_mut()) } == 0
}

fn previous_action() -> &'static libc::sigaction {
    // SAFETY: Installation initializes this record before publishing ACTIVE_CONTROL.
    unsafe { &*ptr::addr_of!(PREVIOUS_ACTION).cast::<libc::sigaction>() }
}

fn restore_action(previous: &libc::sigaction) {
    // SAFETY: Best-effort restoration during worker teardown.
    let _ = unsafe { libc::sigaction(libc::SIGBUS, previous, ptr::null_mut()) };
}

fn restore_stack(previous: &libc::stack_t) {
    // SAFETY: Best-effort restoration during worker teardown.
    let _ = unsafe { libc::sigaltstack(previous, ptr::null_mut()) };
}

fn read_u32(base: *mut u8, at: usize) -> u32 {
    // SAFETY: Fixed aligned field in the mapped control record.
    unsafe { ptr::read_volatile(base.add(at).cast::<u32>()) }
}

fn read_u64(base: *mut u8, at: usize) -> u64 {
    // SAFETY: Fixed aligned field in the mapped control record.
    unsafe { ptr::read_volatile(base.add(at).cast::<u64>()) }
}

fn write_u32(base: *mut u8, at: usize, value: u32) {
    // SAFETY: Fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<u32>(), value) }
}

fn write_i32(base: *mut u8, at: usize, value: i32) {
    // SAFETY: Fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<i32>(), value) }
}

fn write_u64(base: *mut u8, at: usize, value: u64) {
    // SAFETY: Fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<u64>(), value) }
}

fn atomic_u32(base: *mut u8, at: usize) -> &'static AtomicU32 {
    // SAFETY: Fixed aligned atomic field in the mapped control record.
    unsafe { &*base.add(at).cast::<AtomicU32>() }
}

#[cfg(test)]
mod tests {
    use std::fs::OpenOptions;
    use std::os::unix::fs::OpenOptionsExt;
    use std::process::Command;

    use crate::contract::PAGE_SIZE;
    use crate::mapping::Mapping;

    use super::*;

    const CASE_ENV: &str = "IPRANGE_V4_SIGBUS_CASE";
    const CONTROL_ENV: &str = "IPRANGE_V4_SIGBUS_CONTROL";

    #[test]
    fn signal_chain_subprocess_matrix() {
        let native_reset = child_status("native-reset");
        let native_reset_code = native_reset.code().unwrap();
        assert!(matches!(native_reset_code, 86 | 90));
        // Darwin records SA_RESETHAND internally but omits it from the old
        // action returned by sigaction, so no chaining handler can recover it.
        let reset_chain_code = if cfg!(target_vendor = "apple") {
            86
        } else {
            native_reset_code
        };
        let captured_reset_code = if cfg!(target_vendor = "apple") {
            93
        } else {
            92
        };
        for (case, expected) in [
            ("owned", Expected::Exit(OWNED_FAULT_EXIT)),
            ("user-one", Expected::Exit(81)),
            ("user-siginfo", Expected::Exit(82)),
            ("user-mask", Expected::Exit(88)),
            ("user-nodefer", Expected::Exit(89)),
            ("user-reset", Expected::Exit(reset_chain_code)),
            ("captured-reset", Expected::Exit(captured_reset_code)),
            ("unarmed", Expected::Exit(83)),
            ("out-of-region", Expected::Exit(83)),
            ("stale-region", Expected::Exit(83)),
            ("nested", Expected::Exit(83)),
            ("null-info", Expected::Exit(86)),
            ("replacement", Expected::Exit(91)),
            ("default", Expected::Signal(libc::SIGBUS)),
            ("ignore", Expected::Exit(84)),
        ] {
            let status = child_status(case);
            match expected {
                Expected::Exit(code) => assert_eq!(status.code(), Some(code), "case {case}"),
                Expected::Signal(signal) => {
                    use std::os::unix::process::ExitStatusExt;
                    assert_eq!(status.signal(), Some(signal), "case {case}");
                }
            }
        }
    }

    fn child_status(case: &str) -> std::process::ExitStatus {
        Command::new(std::env::current_exe().unwrap())
            .arg("--exact")
            .arg("worker::posix::tests::signal_chain_child")
            .arg("--nocapture")
            .env(CASE_ENV, case)
            .status()
            .unwrap()
    }

    #[test]
    fn signal_chain_child() {
        let Ok(case) = std::env::var(CASE_ENV) else {
            return;
        };
        run_child(&case);
    }

    #[test]
    fn owned_fault_record_survives_worker_exit() {
        let control = super::super::control::Control::create_parent().unwrap();
        let status = Command::new(std::env::current_exe().unwrap())
            .arg("--exact")
            .arg("worker::posix::tests::owned_fault_record_child")
            .arg("--nocapture")
            .env(CONTROL_ENV, control.path())
            .status()
            .unwrap();
        assert_eq!(status.code(), Some(OWNED_FAULT_EXIT));
        let fault = control.fault_record().unwrap();
        assert_eq!(fault.role, super::super::control::MappingRole::Scratch);
        let native_page = native_page_size();
        assert_eq!(fault.relative, native_page as u64);
        assert_eq!(fault.mapping_len, (2 * native_page) as u64);
    }

    #[test]
    fn owned_fault_record_child() {
        let Some(path) = std::env::var_os(CONTROL_ENV) else {
            return;
        };
        let control = super::super::control::Control::open_worker(path.as_ref()).unwrap();
        let _handler = Handler::install(&control).unwrap();
        let mapping = fault_mapping("record");
        let (base, len) = mapping.region().unwrap();
        control
            .arm(41, super::super::control::MappingRole::Scratch, base, len)
            .unwrap();
        fault(&mapping)
    }

    enum Expected {
        Exit(i32),
        Signal(i32),
    }

    fn run_child(case: &str) -> ! {
        install_previous(case);
        if case == "native-reset" {
            unsafe { libc::raise(libc::SIGBUS) };
            unsafe { libc::_exit(84) }
        }
        let mut control = super::super::control::Control::create_parent().unwrap();
        control.remove_path().unwrap();
        let handler = Handler::install(&control).unwrap();
        if case == "captured-reset" {
            let captured =
                handler.previous_action.sa_flags as libc::c_int & libc::SA_RESETHAND != 0;
            unsafe { libc::_exit(if captured { 92 } else { 93 }) }
        }
        match case {
            "owned" => {
                let mapping = fault_mapping("owned");
                let (base, len) = mapping.region().unwrap();
                control
                    .arm(7, super::super::control::MappingRole::Source, base, len)
                    .unwrap();
                fault(&mapping)
            }
            "user-one" | "user-siginfo" | "user-mask" | "user-nodefer" | "user-reset"
            | "default" | "ignore" => {
                unsafe { libc::raise(libc::SIGBUS) };
                unsafe { libc::_exit(84) }
            }
            "unarmed" => fault(&fault_mapping("unarmed")),
            "out-of-region" => {
                let active = valid_mapping("out-active");
                let (base, len) = active.region().unwrap();
                control
                    .arm(11, super::super::control::MappingRole::Source, base, len)
                    .unwrap();
                fault(&fault_mapping("out-fault"))
            }
            "stale-region" => {
                let stale = fault_mapping("stale-fault");
                let (base, len) = stale.region().unwrap();
                control
                    .arm(13, super::super::control::MappingRole::Source, base, len)
                    .unwrap();
                control.disarm();
                let active = valid_mapping("stale-active");
                let (base, len) = active.region().unwrap();
                control
                    .arm(14, super::super::control::MappingRole::Source, base, len)
                    .unwrap();
                fault(&stale)
            }
            "nested" => {
                let mapping = fault_mapping("nested");
                let (base, len) = mapping.region().unwrap();
                control
                    .arm(15, super::super::control::MappingRole::Source, base, len)
                    .unwrap();
                let fields = Control::fault_fields();
                atomic_u32(control.base(), fields.handling).store(1, Ordering::Release);
                fault(&mapping)
            }
            "null-info" => {
                signal_handler(libc::SIGBUS, ptr::null_mut(), ptr::null_mut());
                unsafe { libc::_exit(86) }
            }
            "replacement" => {
                let mut action = unsafe { mem::zeroed::<libc::sigaction>() };
                unsafe { libc::sigemptyset(&mut action.sa_mask) };
                action.sa_sigaction = replacement_siginfo as *const () as usize;
                action.sa_flags = libc::SA_SIGINFO as _;
                assert_eq!(
                    unsafe { libc::sigaction(libc::SIGBUS, &action, ptr::null_mut()) },
                    0
                );
                assert!(handler.verify_owned().is_err());
                drop(handler);
                unsafe { libc::raise(libc::SIGBUS) };
                unsafe { libc::_exit(86) }
            }
            _ => unsafe { libc::_exit(85) },
        }
    }

    fn install_previous(case: &str) {
        let mut action = unsafe { mem::zeroed::<libc::sigaction>() };
        unsafe { libc::sigemptyset(&mut action.sa_mask) };
        match case {
            "user-one" => action.sa_sigaction = one_argument as *const () as usize,
            "user-mask" => {
                action.sa_sigaction = masked_siginfo as *const () as usize;
                action.sa_flags = libc::SA_SIGINFO as _;
                unsafe { libc::sigaddset(&mut action.sa_mask, libc::SIGUSR1) };
            }
            "user-nodefer" => {
                action.sa_sigaction = nodefer_siginfo as *const () as usize;
                action.sa_flags = (libc::SA_SIGINFO | libc::SA_NODEFER) as _;
            }
            "user-reset" | "native-reset" | "captured-reset" => {
                action.sa_sigaction = reset_siginfo as *const () as usize;
                action.sa_flags = (libc::SA_SIGINFO | libc::SA_RESETHAND) as _;
            }
            "default" => action.sa_sigaction = libc::SIG_DFL,
            "ignore" => action.sa_sigaction = libc::SIG_IGN,
            _ => {
                action.sa_sigaction = siginfo as *const () as usize;
                action.sa_flags = libc::SA_SIGINFO as _;
            }
        }
        assert_eq!(
            unsafe { libc::sigaction(libc::SIGBUS, &action, ptr::null_mut()) },
            0
        );
    }

    extern "C" fn one_argument(signal: c_int) {
        let code = if signal == libc::SIGBUS { 81 } else { 86 };
        unsafe { libc::_exit(code) }
    }

    extern "C" fn siginfo(signal: c_int, info: *mut libc::siginfo_t, _context: *mut c_void) {
        let code = if signal != libc::SIGBUS || info.is_null() {
            86
        } else if !synchronous_bus_fault(info) {
            82
        } else {
            83
        };
        unsafe { libc::_exit(code) }
    }

    extern "C" fn masked_siginfo(
        _signal: c_int,
        _info: *mut libc::siginfo_t,
        _context: *mut c_void,
    ) {
        let mut mask = unsafe { mem::zeroed::<libc::sigset_t>() };
        let read = unsafe { libc::sigprocmask(libc::SIG_SETMASK, ptr::null(), &mut mask) } == 0;
        let expected = read
            && unsafe { libc::sigismember(&mask, libc::SIGUSR1) } == 1
            && unsafe { libc::sigismember(&mask, libc::SIGBUS) } == 1;
        unsafe { libc::_exit(if expected { 88 } else { 86 }) }
    }

    extern "C" fn nodefer_siginfo(
        _signal: c_int,
        _info: *mut libc::siginfo_t,
        _context: *mut c_void,
    ) {
        let mut mask = unsafe { mem::zeroed::<libc::sigset_t>() };
        let read = unsafe { libc::sigprocmask(libc::SIG_SETMASK, ptr::null(), &mut mask) } == 0;
        let expected = read && unsafe { libc::sigismember(&mask, libc::SIGBUS) } == 0;
        unsafe { libc::_exit(if expected { 89 } else { 86 }) }
    }

    extern "C" fn reset_siginfo(signal: c_int, _info: *mut libc::siginfo_t, _context: *mut c_void) {
        let mut action = unsafe { mem::zeroed::<libc::sigaction>() };
        let read = unsafe { libc::sigaction(signal, ptr::null(), &mut action) } == 0;
        let expected = read && action.sa_sigaction == libc::SIG_DFL;
        unsafe { libc::_exit(if expected { 90 } else { 86 }) }
    }

    extern "C" fn replacement_siginfo(
        _signal: c_int,
        _info: *mut libc::siginfo_t,
        _context: *mut c_void,
    ) {
        unsafe { libc::_exit(91) }
    }

    fn valid_mapping(label: &str) -> Mapping {
        mapping(label, false)
    }

    fn fault_mapping(label: &str) -> Mapping {
        mapping(label, true)
    }

    fn mapping(label: &str, truncate: bool) -> Mapping {
        let path =
            std::env::temp_dir().join(format!(".iprange-v4-signal-{label}-{}", std::process::id()));
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
            .unwrap();
        std::fs::remove_file(path).unwrap();
        let native_page = native_page_size();
        assert_eq!(native_page % PAGE_SIZE, 0);
        file.set_len((2 * native_page) as u64).unwrap();
        let mapping = Mapping::read_only(file, (2 * native_page) as u64).unwrap();
        if truncate {
            mapping.file().set_len(native_page as u64).unwrap();
        }
        mapping
    }

    fn fault(mapping: &Mapping) -> ! {
        let native_page = native_page_size();
        let page = mapping
            .page(
                (native_page / PAGE_SIZE) as u32,
                (2 * native_page / PAGE_SIZE) as u64,
            )
            .unwrap();
        let _ = page.byte(0);
        unsafe { libc::_exit(87) }
    }

    fn native_page_size() -> usize {
        let size = unsafe { libc::sysconf(libc::_SC_PAGESIZE) };
        usize::try_from(size).ok().filter(|size| *size > 0).unwrap()
    }
}
