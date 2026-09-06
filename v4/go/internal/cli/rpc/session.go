// Connection session: reader goroutine, main event loop, bounded
// request queue, cancellation, EOF shutdown, and fatal transport
// failures (Rust rpc/session.rs parity, iprange-jsonrpc-v1.md).
//
// Model: a reader goroutine forwards one physical input frame at a
// time to the main loop; the main loop applies cancellation and queue
// admission; one worker goroutine executes one decoded frame (a
// single request or a batch) at a time; additional frames wait behind
// a 16-request admission counter so the transport never blocks.
//
// Per-request semantics:
//   - one active request plus at most 16 queued; the admission
//     counter counts queued members only and the one active member is
//     not counted, so a member frees its slot when it starts
//     executing; a request exceeding the bound fails with -32002
//     server_busy;
//   - the reader stays active while work executes so cancellation and
//     EOF are observed;
//   - the control plane (cancelled/pending ids, active keys, the
//     cancellation token, shutting-down, fatal-error) is locked
//     separately from connection resources, so a cancel notification,
//     EOF, or termination signal can always cancel the active unit's
//     token while its handler is running;
//   - cancel notifications apply during the frame scan: only ids
//     admitted and not yet terminal are valid targets; a same-batch
//     cancel may target an earlier sibling but not a later one;
//     queued matches are skipped without a response; each executing
//     member runs under its own fresh cancellation token with only
//     its own id marked active, so the active request is signalled
//     only when it contains the cancelled id and a cancelled queued
//     sibling's token can never reach a later member;
//   - a whole frame decodes strictly before anything in it executes;
//     envelope failures produce one id-null error and the service
//     keeps serving;
//   - every complete response object is capped at 65,000 bytes; an
//     unencodable inline success is replaced by the `output_limit`
//     product error, never an oversized frame; a busy-rejected batch
//     element stays inside its batch response array in position;
//   - stdin EOF stops acceptance, cancels queued and active work,
//     lets every admitted unit reach a factual terminal outcome,
//     closes all cursors and registered live readers, and exits 0
//     unless transport shutdown itself failed;
//   - a frame over the input ceiling produces -32001 with id null and
//     the process closes without parsing later bytes, exiting non-zero
//     (startup/framing failure);
//   - an unanswerable id (its echo alone cannot fit the response
//     object ceiling) produces -32001 with id null; the service keeps
//     serving;
//   - broken stdout, termination signals, and stdin read errors are
//     fatal: the session runs the same cancellation/handle-cleanup
//     path as EOF and the process exits non-zero.

package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// sessionControl is the cancellation/shutdown control plane, locked
// separately from SessionState so cancel/EOF always reach the active
// unit's token while a handler runs.
type sessionControl struct {
	// Request ids cancelled by the transport; always a subset of
	// pending.
	cancelled map[string]bool
	// Request ids admitted but not yet terminal; only these are valid
	// cancellation targets.
	pending map[string]bool
	// Set by EOF/fatal shutdown; the worker skips nothing admitted
	// before EOF but fresh tokens are no longer installed.
	shuttingDown bool
	// First worker write failure; makes shutdown exit non-zero when
	// stdout broke while draining queued units.
	fatalWrite error
	// Set when the termination-signal watcher consumes SIGINT/SIGTERM;
	// the EOF exit path reports non-zero when a signal raced EOF.
	terminationSignal bool
	// Cancellation signal for the executing work member; the worker
	// replaces it with a fresh token once per executing member.
	token *iprangedb.CancellationToken
	// Ids of the request member currently executing; empty between
	// members and for non-executing entries.
	activeKeys map[string]bool
}

// SessionState is the connection-owned state shared by handlers.
type SessionState struct {
	// Resources are the shared reader/cursor handles and their
	// connection limits.
	Resources *ConnectionState
	// ActiveRequestID is the id of the request currently executing;
	// cursor handlers size pages against the complete
	// response-object ceiling using this id.
	ActiveRequestID *RequestId

	mu        sync.Mutex // guards Resources and ActiveRequestID
	control   *sessionControl
	controlMu sync.Mutex
}

// NewSessionState builds an empty connection state.
func NewSessionState() *SessionState {
	return &SessionState{
		Resources: NewConnectionState(),
		control:   newSessionControl(),
	}
}

func newSessionControl() *sessionControl {
	return &sessionControl{
		cancelled:  make(map[string]bool),
		pending:    make(map[string]bool),
		token:      iprangedb.NewCancellationToken(),
		activeKeys: make(map[string]bool),
	}
}

// Token returns the executing member's cancellation token. Handlers
// poll this token during long SDK work; the session loop can cancel
// it at any time through the control plane.
func (st *SessionState) Token() *iprangedb.CancellationToken {
	st.controlMu.Lock()
	defer st.controlMu.Unlock()
	return st.control.token
}

// withState runs fn with the connection resources locked (the worker
// holds this lock for the whole duration of a handler call).
func (st *SessionState) withState(fn func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn()
}

// workEntryKind classifies one decoded frame element.
type workEntryKind uint8

const (
	workExecute workEntryKind = iota
	workBusy
	workUnanswerable
)

// workEntry is one element of a decoded frame in frame order. Busy and
// Unanswerable keep their position so the worker emits exactly one
// batch response array whose members follow the request order.
type workEntry struct {
	kind    workEntryKind
	request *Request
}

// workUnit is one decoded frame queued as a unit: array-order
// execution and one response frame per input frame.
type workUnit struct {
	entries []*workEntry
	batch   bool
}

// sessionEvent is one event the transport goroutines report to the
// main session loop.
type sessionEvent struct {
	// line is one physical input frame; a nil line with eof=true is
	// end of input; err carries a line-read failure.
	line []byte
	eof  bool
	err  *LineReadError
	// fatal is an unrecoverable transport failure (broken stdout, a
	// termination signal).
	fatal error
}

// Session runs the JSON-RPC service.
type Session struct {
	state    *SessionState
	inFlight atomic.Int64
	// workTx carries decoded work units to the worker; it is buffered
	// to the queue bound so an admitted unit's send never blocks while
	// the worker executes (inFlight bounds the buffered units).
	workTx chan workUnit
	// workerDone is closed when the worker goroutine exits; shutdown
	// and fatal wait on it before closing connection resources.
	workerDone chan struct{}
	// shutdownDone is closed by beginShutdown. The worker's terminal
	// failure report selects on it: once the main loop stops draining
	// events for shutdown, a blocking send into the full events
	// channel would deadlock the worker join.
	shutdownDone chan struct{}
	// shutdownOnce guards the single close of workTx/shutdownDone:
	// shutdown and fatal are mutually exclusive per Run, but a
	// re-entrant close must stay impossible.
	shutdownOnce sync.Once
}

// NewSession creates a service with one worker.
func NewSession() *Session {
	return &Session{
		state:        NewSessionState(),
		workTx:       make(chan workUnit, QueuedLimit),
		workerDone:   make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
}

// Run executes the service on the given streams until EOF or a fatal
// transport failure.
func (s *Session) Run(reader io.Reader, writer io.Writer) error {
	events := make(chan sessionEvent, 64)
	writerMu := &sync.Mutex{}
	fw := NewFrameWriter(writer)

	// Worker goroutine: executes work units in arrival order.
	go func() {
		defer close(s.workerDone)
		workerLoop(s, fw, writerMu, events)
	}()

	// Reader goroutine: forwards one physical input frame at a time.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		lr := NewLineReader(reader)
		for {
			line, ok, err := lr.ReadLine()
			if err != nil {
				events <- sessionEvent{err: err}
				return
			}
			if !ok {
				events <- sessionEvent{eof: true}
				return
			}
			events <- sessionEvent{line: line}
		}
	}()

	// Termination signals (Unix): report their delivery as a fatal
	// transport failure so the process exits non-zero after the same
	// cancellation/cleanup path as broken stdout. The watcher never
	// selects on reader EOF: a signal observed at any point, including
	// mid-drain after the main loop already chose the EOF path, must
	// still win over the exit-zero path (the main loop checks
	// terminationSignal before returning nil). If the transport is
	// wedged (events channel full, shutdown unreachable because the
	// worker is blocked on a full stdout pipe), a watchdog forces the
	// non-zero exit so a termination signal can never be ignored.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		sig := <-sigCh
		err := errors.New("terminated by signal " + sig.String())
		st := s.state
		st.controlMu.Lock()
		st.control.terminationSignal = true
		if st.control.fatalWrite == nil {
			st.control.fatalWrite = err
		}
		st.controlMu.Unlock()
		delivered := make(chan struct{})
		go func() {
			reportFatal(events, s, err)
			close(delivered)
		}()
		select {
		case <-delivered:
			// The main loop took the fatal path, or shutdown already
			// began and the report became abortable.
		case <-time.After(signalForceExitTimeout):
			fmt.Fprintf(os.Stderr, "iprange: %v: transport wedged, forcing exit\n", err)
			os.Exit(1)
		}
	}()

	var runErr error
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			switch {
			case ev.fatal != nil:
				runErr = s.fatal(ev.fatal, writer, fw)
				break loop
			case ev.err != nil:
				if ev.err.FrameTooLarge {
					payload := (&SchemaError{Code: TransportFrameTooLarge, Message: "frame over input limit"}).Response(nil)
					writerMu.Lock()
					werr := fw.WriteLine(string(payload))
					writerMu.Unlock()
					if werr != nil {
						runErr = s.fatal(werr, writer, fw)
					} else {
						// Framing failures exit non-zero (spec
						// iprange-jsonrpc-v1.md shutdown section): drain
						// queued work and close resources exactly like EOF,
						// then report the framing failure.
						if err := s.shutdown(); err != nil {
							runErr = err
						} else {
							runErr = errors.New("frame over input limit: framing failure")
						}
					}
					break loop
				}
				runErr = s.fatal(errors.New("stdin read failed: "+ev.err.Error()), writer, fw)
				break loop
			case ev.eof:
				runErr = s.shutdown()
				break loop
			default:
				if err := s.handleFrame(ev.line, fw, writerMu); err != nil {
					runErr = s.fatal(err, writer, fw)
					break loop
				}
			}
		}
	}
	// Wait for the worker to drain before closing resources. If the
	// worker exited on its own (broken stdout reports fatal through
	// the events channel and sets runErr; the channel close alone
	// cannot happen without shutdown) this wait still terminates.
	<-s.workerDone
	if runErr == nil {
		// A termination signal observed before or during EOF always
		// wins over the exit-zero EOF path (termination signals are
		// fatal transport failures; the EOF branch may have raced the
		// watcher's event delivery with the EOF event).
		st := s.state
		st.controlMu.Lock()
		terminated := st.control.terminationSignal
		err := st.control.fatalWrite
		st.controlMu.Unlock()
		if terminated && err != nil {
			runErr = err
		}
	}
	select {
	case <-sigDone:
	default:
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

// applyCancel cancels one request id during the frame scan.
func (s *Session) applyCancel(request *Request) {
	if !validCancelParams(request.Params) {
		return
	}
	var cancelID string
	obj := map[string]json.RawMessage{}
	if json.Unmarshal(request.Params, &obj) != nil {
		return
	}
	raw, ok := obj["request_id"]
	if !ok {
		return
	}
	if isStringRaw(raw) {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return
		}
		cancelID = "s:" + text
	} else {
		key, ok := numberCancelKey(raw)
		if !ok {
			return
		}
		cancelID = key
	}
	st := s.state
	st.controlMu.Lock()
	defer st.controlMu.Unlock()
	if !st.control.pending[cancelID] {
		return
	}
	st.control.cancelled[cancelID] = true
	if st.control.activeKeys[cancelID] {
		st.control.token.Cancel()
	}
}

// beginShutdown marks the session shutting down and cancels the
// active token, then closes the work channel so the worker drains and
// exits.
func (s *Session) beginShutdown() {
	st := s.state
	st.controlMu.Lock()
	st.control.shuttingDown = true
	st.control.token.Cancel()
	st.controlMu.Unlock()
	s.shutdownOnce.Do(func() {
		close(s.workTx)
		close(s.shutdownDone)
	})
}

// closeRegisteredResources closes every connection resource after the
// worker joined.
func (s *Session) closeRegisteredResources() error {
	failures := s.state.Resources.CloseAll()
	if len(failures) == 0 {
		return nil
	}
	message := "transport shutdown failed to close a live reader"
	for _, failure := range failures {
		message += "; " + failure
	}
	return errors.New(message)
}

// shutdown is the EOF path: stop acceptance, cancel queued and active
// work, wait for the worker, close resources; zero unless the
// transport itself failed.
func (s *Session) shutdown() error {
	s.beginShutdown()
	<-s.workerDone
	var workerErr error
	s.state.controlMu.Lock()
	workerErr = s.state.control.fatalWrite
	s.state.controlMu.Unlock()
	closeErr := s.closeRegisteredResources()
	switch {
	case workerErr != nil && closeErr != nil:
		return errors.New(workerErr.Error() + "; " + closeErr.Error())
	case workerErr != nil:
		return workerErr
	case closeErr != nil:
		return closeErr
	}
	return nil
}

// fatal is the fatal-failure path: same cancellation/handle cleanup as
// EOF, then report the failure (non-zero exit).
func (s *Session) fatal(err error, _ io.Writer, _ *FrameWriter) error {
	s.beginShutdown()
	<-s.workerDone
	closeErr := s.closeRegisteredResources()
	if closeErr != nil {
		return errors.New(err.Error() + "; " + closeErr.Error())
	}
	return err
}

// signalForceExitTimeout bounds how long a termination signal waits
// for the graceful fatal path before the watchdog forces the process
// to exit non-zero. The events channel can be permanently full and
// shutdown unreachable (worker blocked on a full stdout pipe, main
// loop blocked on the full work queue); never leave the operator
// without a working termination signal.
const signalForceExitTimeout = 500 * time.Millisecond

// reportFatal delivers the worker's terminal failure to the main
// loop without ever blocking past shutdown. The main loop stops
// draining events once it enters its terminal path and then joins the
// worker; if the events channel were full at that moment, a blocking
// send would deadlock the join (the worker waits for a drained slot,
// the main loop waits for the worker). Selecting on shutdownDone
// makes the report abortable exactly when the main loop stops
// receiving, while the recorded control.fatalWrite error still
// determines the non-zero exit.
func reportFatal(events chan<- sessionEvent, s *Session, err error) {
	select {
	case events <- sessionEvent{fatal: err}:
	case <-s.shutdownDone:
	}
}

// workerLoop executes work units until the work channel closes.
func workerLoop(s *Session, fw *FrameWriter, writerMu *sync.Mutex, events chan<- sessionEvent) {
	for unit := range s.workTx {
		// Unit-level terminal key set: every execute id of the unit is
		// removed from the cancellation/pending tables once all members
		// have run.
		keys := make(map[string]bool)
		for _, entry := range unit.entries {
			if entry.kind == workExecute && entry.request.ID != nil {
				keys[entry.request.ID.Key()] = true
			}
		}
		st := s.state
		responses := make([]json.RawMessage, 0, len(unit.entries))
		for _, entry := range unit.entries {
			// A member frees its queue slot when it starts executing;
			// earlier members of the same unit stay counted until then.
			// Busy and unanswerable entries never occupied a slot.
			if entry.kind == workExecute {
				s.inFlight.Add(-1)
			}
			// Per-member cancellation scope: the executing member runs
			// under its own fresh token with only its own id marked active,
			// so cancelling a queued sibling cannot reach this member's
			// token and a cancelled member's token cannot poison later
			// siblings. Token/flag update in one control-lock scope: if
			// shutdown lands between a check and a fresh token install, the
			// fresh token would escape cancellation; while shutting down the
			// already-cancelled token is kept so queued work after EOF keeps
			// aborting factually.
			st.controlMu.Lock()
			if entry.kind == workExecute && !st.control.shuttingDown {
				st.control.token = iprangedb.NewCancellationToken()
			}
			active := make(map[string]bool)
			if entry.kind == workExecute && entry.request.ID != nil {
				active[entry.request.ID.Key()] = true
			}
			st.control.activeKeys = active
			st.controlMu.Unlock()

			if resp, ok := entryResponse(s, entry); ok {
				responses = append(responses, resp)
			}

			// The member is no longer active; a cancel arriving now can only
			// mark its id (driving the omit path for members still queued in
			// this unit), never cancel a token.
			st.controlMu.Lock()
			st.control.activeKeys = make(map[string]bool)
			st.controlMu.Unlock()
		}
		// Terminal state: the unit's ids are no longer cancellation
		// targets, so a later request reusing an id starts clean.
		st.controlMu.Lock()
		for key := range keys {
			delete(st.control.cancelled, key)
			delete(st.control.pending, key)
		}
		st.control.activeKeys = make(map[string]bool)
		st.controlMu.Unlock()

		if len(responses) == 0 {
			continue
		}
		var payload any
		if unit.batch {
			arr := make([]any, len(responses))
			for i, r := range responses {
				arr[i] = json.RawMessage(r)
			}
			payload = arr
		} else {
			payload = json.RawMessage(responses[0])
		}
		text, serr := encodeResponseFrame(payload)
		if serr != nil {
			// Bounded by construction; treat as fatal.
			err := errors.New("response frame encoding failed: " + serr.Message)
			st.controlMu.Lock()
			st.control.fatalWrite = err
			st.controlMu.Unlock()
			reportFatal(events, s, err)
			return
		}
		writerMu.Lock()
		werr := fw.WriteLine(text)
		writerMu.Unlock()
		if werr != nil {
			err := FatalWriteError(werr)
			st.controlMu.Lock()
			st.control.fatalWrite = err
			st.controlMu.Unlock()
			reportFatal(events, s, err)
			return
		}
	}
}

// handleFrame decodes one input frame, applies cancellation, admits
// requests, and queues or directly answers the work unit.
func (s *Session) handleFrame(line []byte, fw *FrameWriter, writerMu *sync.Mutex) error {
	requests, serr := DecodeFrame(line)
	if serr != nil {
		payload := serr.Response(nil)
		writerMu.Lock()
		werr := fw.WriteLine(string(payload))
		writerMu.Unlock()
		return werr
	}
	var entries []*workEntry
	batch := false
	for _, request := range requests {
		if request.Method == CancelMethod {
			s.applyCancel(request)
			continue
		}
		batch = batch || request.BatchIndex != nil
		entry := admitOne(request, &s.inFlight)
		if entry.kind == workExecute {
			markPending(s.state, entry)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil
	}
	admitted := 0
	for _, entry := range entries {
		if entry.kind == workExecute {
			admitted++
		}
	}
	if batch {
		if admitted == 0 {
			// Every element was rejected at admission (busy or
			// unanswerable): answer the whole array immediately so an
			// all-rejected batch never occupies queue capacity and the
			// dispatcher cannot block behind a pipelined flood (Rust
			// session.rs parity, where the unbounded channel never
			// blocks either).
			responses := make([]json.RawMessage, 0, len(entries))
			for _, entry := range entries {
				if resp, ok := entryResponse(s, entry); ok {
					responses = append(responses, resp)
				}
			}
			arr := make([]any, len(responses))
			for i, r := range responses {
				arr[i] = json.RawMessage(r)
			}
			text, err := encodeResponseFrame(arr)
			if err != nil {
				return errors.New("bounded response encoding failed")
			}
			writerMu.Lock()
			werr := fw.WriteLine(text)
			writerMu.Unlock()
			return werr
		}
		s.workTx <- workUnit{entries: entries, batch: true}
		return nil
	}
	switch entries[0].kind {
	case workBusy:
		payload := busyResponse(entries[0].request)
		text, err := encodeResponseFrame(payload)
		if err != nil {
			return errors.New("bounded response encoding failed")
		}
		writerMu.Lock()
		werr := fw.WriteLine(text)
		writerMu.Unlock()
		return werr
	case workUnanswerable:
		payload := unanswerableResponse()
		text, _ := encodeResponseFrame(payload)
		writerMu.Lock()
		werr := fw.WriteLine(text)
		writerMu.Unlock()
		return werr
	default:
		s.workTx <- workUnit{entries: entries, batch: false}
		return nil
	}
}

// markPending records one admitted Execute key as a valid
// cancellation target during the frame scan.
func markPending(st *SessionState, entry *workEntry) {
	if entry.request.ID == nil {
		return
	}
	st.controlMu.Lock()
	st.control.pending[entry.request.ID.Key()] = true
	st.controlMu.Unlock()
}

// admitOne admits one ordinary frame element against the queue bound.
func admitOne(request *Request, inFlight *atomic.Int64) *workEntry {
	if preflightUnanswerableID(request) {
		return &workEntry{kind: workUnanswerable, request: request}
	}
	if inFlight.Load() >= QueuedLimit {
		return &workEntry{kind: workBusy, request: request}
	}
	inFlight.Add(1)
	return &workEntry{kind: workExecute, request: request}
}

func busyResponse(request *Request) json.RawMessage {
	return ErrorResponse(request.ID, TransportServerBusy, "server_busy", nil)
}

func unanswerableResponse() json.RawMessage {
	return (&SchemaError{
		Code:    TransportFrameTooLarge,
		Message: "request id cannot be echoed within the response object limit",
	}).Response(nil)
}

// entryResponse builds one frame-ordered response object, or ok=false
// for a request cancelled before execution (omitted from a batch).
func entryResponse(s *Session, entry *workEntry) (json.RawMessage, bool) {
	switch entry.kind {
	case workExecute:
		request := entry.request
		cancelled := false
		if request.ID != nil {
			st := s.state
			st.controlMu.Lock()
			cancelled = st.control.cancelled[request.ID.Key()]
			st.controlMu.Unlock()
		}
		if cancelled {
			return nil, false
		}
		return boundedResponse(execute(s, request), request), true
	case workBusy:
		return boundedResponse(busyResponse(entry.request), entry.request), true
	case workUnanswerable:
		return unanswerableResponse(), true
	}
	return nil, false
}

// boundedResponse enforces the 65,000-byte ceiling on the complete
// response object. An oversized success is replaced by the documented
// output_limit product error; an oversized product error keeps its
// stable data.code/data.outcome and drops free-form details.
func boundedResponse(response json.RawMessage, request *Request) json.RawMessage {
	// Success fast path: the envelope bytes are exactly what the
	// handlers produced, so the ceiling is a length check; the
	// parse-and-reencode path below runs only for genuinely oversized
	// responses (Rust session.rs bounded_response parity, zero parses
	// on the hot path).
	if len(response) <= ResponseObjectLimit {
		return response
	}
	var payload any
	if err := json.Unmarshal(response, &payload); err != nil {
		// Built envelopes always marshal; defensive fallback.
		return unanswerableResponse()
	}
	if _, serr := encodeResponseObject(payload); serr == nil {
		return response
	}
	// Try the reduced product-error form.
	if obj, ok := payload.(map[string]any); ok {
		if errObj, ok := obj["error"].(map[string]any); ok {
			if data, ok := errObj["data"].(map[string]any); ok {
				reduced := map[string]any{}
				if code, ok := data["code"]; ok {
					reduced["code"] = code
				}
				if outcome, ok := data["outcome"]; ok {
					reduced["outcome"] = outcome
				}
				errObj["data"] = reduced
				if _, serr := encodeResponseObject(obj); serr == nil {
					return mustMarshal(obj)
				}
			}
		}
	}
	replacement := ErrorResponse(request.ID, ProductError,
		"response object exceeds the 65000-byte limit", outputLimitErrorData())
	if _, serr := encodeRawResponseObject(replacement); serr == nil {
		return replacement
	}
	return (&SchemaError{
		Code:    TransportFrameTooLarge,
		Message: "response object exceeds the 65000-byte limit; request id cannot be echoed",
	}).Response(nil)
}

// encodeRawResponseObject is the raw-message form of the object
// ceiling check used on already-built envelopes.
func encodeRawResponseObject(raw json.RawMessage) (string, *SchemaError) {
	if len(raw) > ResponseObjectLimit {
		return "", &SchemaError{Code: TransportFrameTooLarge, Message: "response object over 65,000-byte limit"}
	}
	return string(raw), nil
}

// execute resolves, validates, and runs one request's handler.
func execute(s *Session, request *Request) json.RawMessage {
	validator, handler, ok := resolve(request.Method)
	if !ok {
		return ErrorResponse(request.ID, StdMethodNotFound, "unknown method "+request.Method, nil)
	}
	if err := validator(request.Params); err != nil {
		return ErrorResponse(request.ID, StdInvalidParams, err.Error(), nil)
	}
	var result any
	var herr *HandlerError
	st := s.state
	st.mu.Lock()
	st.ActiveRequestID = request.ID
	result, herr = handler(st, request.Params)
	st.ActiveRequestID = nil
	st.mu.Unlock()
	if herr == nil {
		return SuccessResponse(request.ID, result)
	}
	data := map[string]any{"code": herr.Code, "outcome": herr.Outcome}
	if herr.Details != nil {
		data["details"] = herr.Details
	}
	return ErrorResponse(request.ID, ProductError, herr.Message, data)
}

// preflightUnanswerableID reports whether the request id alone makes
// even the smallest faithful response exceed the response-object
// ceiling; such an id can never be answered and is answered -32001
// with id null (the service keeps serving).
func preflightUnanswerableID(request *Request) bool {
	if request.ID == nil {
		return false
	}
	probe := ErrorResponse(request.ID, ProductError,
		"response object exceeds the 65000-byte limit", outputLimitErrorData())
	_, serr := encodeRawResponseObject(probe)
	return serr != nil
}
