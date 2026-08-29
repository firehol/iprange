//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package worker

// tryWait reports whether the child exited without blocking (Rust
// Process::try_wait): the portable reaper's buffered channel is
// polled; a reap consumes the child and records the status, and a
// child that was already reaped keeps returning its recorded status.
// This is the platform-neutral equivalent of wait4(WNOHANG) and
// GetExitCodeProcess.
func (p *Process) tryWait() (*exitStatus, bool, error) {
	if p.status != nil {
		return p.status, true, nil
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return nil, false, nil
	}
	select {
	case status := <-p.done:
		p.cmd = nil
		p.status = &status
		return p.status, true, nil
	default:
		return nil, false, nil
	}
}
