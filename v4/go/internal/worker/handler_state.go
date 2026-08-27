//go:build linux || darwin || freebsd || windows

package worker

// activeControl publishes the armed control base after the handler is
// installed (Rust posix.rs / windows.rs ACTIVE_CONTROL). The naked
// handler and the session probe read it; each platform's handler
// machine publishes it with a release-equivalent store on install and
// clears it on close.
var activeControl uintptr
