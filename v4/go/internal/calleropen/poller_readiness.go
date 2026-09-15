// Poller-readiness decision (SOW-0028 wave-19.25 design section 6).
//
// The runtime network poller's creation has no failure path: the first
// pollable registration or timer arm under an exhausted table aborts the
// process (runtime/netpoll_epoll.go:26,:31 print "runtime: … failed" and
// raise a fatal error, which is not an error path product code can
// observe or recover from). Owning the persistent opens, the waits and the
// entropy draws removes every trigger this process reaches by accident;
// the one trigger that cannot be removed is host-name resolution, which
// reaches netpollinit through the net package. This package therefore owns
// exactly one decision per process — whether the poller can be created —
// taken while the process is still single-goroutine, plus the deliberate
// creation itself on the only path that can need it.
//
// The decision is not a budget: nothing is reserved on behalf of a
// handler, no request is refused because of it, and it cannot be revised.
// When it records "unavailable", the only effect is that the resolver
// declines to run (internal/cli/fileio consults ResolverAllowed), so
// nothing enters net and the process can never reach the eventfd throw.
package calleropen

import (
	"sync"
	"time"
)

// pollerDemandFree is the number of free descriptors the decision requires
// before the poller may be created. Measured on Linux/amd64 with
// CGO_ENABLED=0: the poller allocates exactly two descriptors once
// (epoll_create1 then eventfd, runtime/netpoll_epoll.go:21-31) and the
// resolver's own datagram socket takes one more, so four free slots cover
// the creation plus the request that provokes it with margin. Other
// platforms keep the same conservative number until their own measurement
// exists (design section 6 platform clause): the kqueue poller needs one
// descriptor and the Windows I/O completion port none, so over-reserving
// there is safe.
const pollerDemandFree = 4

// pollerInitProbe is the deliberate timer arm that creates the poller. Any
// runtime timer creation runs netpollGenericInit (runtime/time.go:455-461),
// so the shortest real sleep is the cheapest provoking arm; it must be a
// time.Sleep, not this package's nanosleep owner, precisely because the
// point is to enter the runtime timer path.
const pollerInitProbe = 2 * time.Millisecond

var (
	pollerDecisionOnce sync.Once
	pollerDecision     pollerDecisionState
	pollerInitOnce     sync.Once
)

type pollerDecisionState struct {
	ready   bool
	decided bool
}

// InitPollerReadiness takes the one poller-readiness decision for the
// process. It is idempotent and must be called from the transport session's
// entry, before it starts its goroutines and before the first frame is
// read, so the table read is atomic with respect to the process's own
// activity. Calls after that point (the resolver's lazy consultation on the
// legacy surface, for example) observe the recorded decision.
//
// Taking the decision creates nothing: the poller's two descriptors are
// permanent and no arm besides the resolver can reach it, so creating it
// here would charge every other operation for two descriptors it never
// asked for (design section 10's poller-free promise for every arm except
// the resolver arm). The creation belongs to initPoller, below.
func InitPollerReadiness() {
	pollerDecisionOnce.Do(func() {
		free, ok := FreeDescriptors()
		pollerDecision.decided = true
		// A table read that cannot be taken is answered "unavailable":
		// the resolver declines, which is the conservative direction, and
		// no other arm is affected.
		pollerDecision.ready = ok && free >= pollerDemandFree
	})
}

// PollerReadiness reports the recorded decision and whether it has been
// taken. An untaken decision is only observable before the session entry
// (or on the legacy surface before the first resolver call); callers treat
// it as not ready.
func PollerReadiness() (ready, decided bool) {
	return pollerDecision.ready, pollerDecision.decided
}

// ResolverAllowed reports whether host-name resolution may enter net.
//
// Three conditions, all owned here: the process-wide readiness decision was
// ready (section 6); the table still has pollerDemandFree free slots at the
// moment of the call, because the handler's own opens happened after the
// decision and the poller's creation is unfailable; and the deliberate
// creation then runs, so the runtime is never provoked into creating the
// poller from inside net where no owner exists. It takes the decision on
// demand so a surface that reaches the resolver without a session still
// answers from a real decision rather than a guess.
//
// Returning false is not a refusal of the request: the caller answers the
// class the reference answers for a lookup that could not be performed
// (input_format with outcome not_started), before anything enters net.
func ResolverAllowed() bool {
	InitPollerReadiness()
	if !pollerDecision.ready {
		return false
	}
	free, ok := FreeDescriptors()
	if !ok || free < pollerDemandFree {
		return false
	}
	initPoller()
	return true
}

// initPoller creates the network poller deliberately, once, inside a window
// whose headroom was just proven. Idempotent (the runtime guards it with
// netpollInited), and every later registration joins the existing poller
// instead of being the call that fails.
func initPoller() {
	pollerInitOnce.Do(func() {
		time.Sleep(pollerInitProbe)
	})
}
