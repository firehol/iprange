# SOW-0029 - JSON-RPC WebSocket Daemon

## Status

Status: open

Sub-state: deliberately deferred behind SOW-0028 by user decision 1A on
2026-08-28. The production JSON-RPC method registry must be implemented and
accepted over stdio before this network transport is designed or coded.

## Requirements

### Purpose

Expose the accepted `iprange` JSON-RPC application API through a production
WebSocket daemon without changing method semantics and without granting remote
clients uncontrolled filesystem authority.

### User Request

- Permit applications to connect to an `iprange --daemon` WebSocket service
  and use the same JSON-RPC interface available through `iprange --jsonrpc`.
- Keep daemon implementation separate from SOW-0028 so the production stdio
  API, legacy compatibility, and external qualification can complete first.

### Assistant Understanding

Facts:

- SOW-0028 owns JSON-RPC method names, parameters, results, errors,
  cancellation, and logical production behavior.
- A network daemon adds listener, authentication, authorization, path
  confinement, TLS/origin, multi-client concurrency, quotas, shutdown, and
  operational responsibilities absent from a same-user subprocess.
- Methods can read, create, replace, validate, recover, and remove local files.
  Exposing them without a strict authority model is unsafe.

Inferences:

- The daemon must be a transport/authority adapter over the SOW-0028 dispatcher,
  never a second method implementation.
- Local-only and remote-capable deployments have different implications and
  require an explicit user decision after the stdio API exists.

Unknowns:

- Listener scope/defaults: local Unix/named-pipe bridge, loopback TCP, or
  explicitly remote TCP.
- TLS termination, authentication, credential rotation, origin checks, and
  authorization.
- Filesystem root and safe path resolution for input, output, recovery, and
  cleanup.
- Multi-client scheduling, per-client handles, quotas, rate limits,
  backpressure, and graceful restart.

### Acceptance Criteria

- The daemon reuses the accepted SOW-0028 dispatcher and schemas.
- Every listener has an explicit documented authority and path policy.
- Authentication, transport security, origin policy, quotas, concurrency,
  cancellation, shutdown, and diagnostics are specified before code.
- Connection handles cannot cross clients and are released on disconnect.
- Remote input cannot escape its authorized namespace or cause unbounded
  compute, memory, disk, descriptors, queues, or response buffering.
- The same external semantic cases run through stdio and WebSocket; daemon-only
  cases cover authentication, authorization, isolation, backpressure,
  disconnect, restart, and denial-of-service boundaries.
- Specs, operator docs, packaging, service examples, and project skills
  describe the delivered security and operational model.

## Analysis

Sources checked:

- SOW-0028 production JSON-RPC design.
- JSON-RPC 2.0 specification, `https://www.jsonrpc.org/specification`
  (accessed 2026-08-28).

Current state:

- No product JSON-RPC dispatcher or daemon exists.
- Listener/authority decisions would be speculative until the accepted stdio
  contract and measured resource behavior exist.

Risks:

- Unauthorized local-file read, write, replacement, recovery, or deletion.
- Credential leakage, cross-origin access, cleartext remote traffic, path
  traversal, symlink escape, request amplification, disk exhaustion, handle
  leaks, client starvation, and unsafe restart during mutation.
- Semantic drift if WebSocket handlers reimplement SOW-0028 methods.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- WebSocket can carry the same JSON-RPC messages, but changes the trust boundary
  from a child process controlled by one caller to a service shared by clients.
  Safe implementation depends on an accepted registry and explicit listener,
  identity, authorization, path, and resource decisions.

Evidence reviewed:

- SOW-0028 records the approved method/transport split.
- No current daemon code or deployment policy exists to reuse.

Affected contracts and surfaces:

- CLI invocation, WebSocket/HTTP upgrade, connection context,
  authentication/authorization, filesystem authority, concurrency, quotas,
  observability, packaging, services, docs, specs, skills, tests, and gates.

Existing patterns to reuse:

- The accepted SOW-0028 dispatcher, errors, cancellation, handle registry,
  external cases, and production limits.

Risk and blast radius:

- Security and operational blast radius is high because authorized methods
  mutate durable local state.
- No v4 byte-format change is expected or authorized.

Sensitive data handling plan:

- Use synthetic credentials, paths, addresses, and metadata in committed tests
  and docs. Never commit real tokens, private endpoints, customer information,
  or operational deployment data.

Implementation plan:

1. Complete and accept SOW-0028.
2. Investigate and present numbered listener/security/path/concurrency options
   with evidence.
3. Record user decisions and complete this pre-implementation gate.
4. Specify, implement, qualify, document, and independently review the daemon
   without changing JSON-RPC method semantics.

Validation plan:

- Pending the security/deployment decisions above.
- Reuse SOW-0028 semantic cases and add transport/security/resource cases.
- Run native platforms only after explicit user authorization.

Artifact impact plan:

- AGENTS.md: update daemon workflow/security guardrails after delivery.
- Runtime project skills: update operating/review procedure after delivery.
- Specs: add exact daemon transport and authority contract.
- End-user/operator docs: add deployment, authentication, TLS/origin, paths,
  quotas, monitoring, shutdown, and recovery guidance.
- End-user/operator skills: reassess after an operator workflow exists.
- SOW lifecycle: remain pending until SOW-0028 completes; never execute both as
  one batch.

Open-source reference evidence:

- No external daemon was selected or inspected because listener/security design
  is intentionally unresolved until SOW-0028. That investigation is required
  in the future gate.

Open decisions:

1. Blocking after SOW-0028: listener scope and remote-access policy.
2. Blocking after SOW-0028: authentication, TLS/origin, and authorization.
3. Blocking after SOW-0028: filesystem root and safe path model.
4. Blocking after SOW-0028: concurrency, quotas, backpressure, and operations.

## Implications And Decisions

1. **Separate daemon SOW** - user decision 1A on 2026-08-28.
   - Selection: finish production stdio JSON-RPC first; implement WebSocket
     separately.
   - Benefit: one stable method contract and focused security design.
   - Implication: `--daemon` is unsupported until this SOW completes.
   - Recommendation class: long-term-best.

## Plan

1. Wait for SOW-0028 acceptance.
2. Resolve four security/operational decisions.
3. Complete design, implementation, qualification, docs, and review.

## Execution Log

### 2026-08-28

- Created this pending follow-up from user decision 1A.
- No daemon, listener, authentication, authorization, or product code changed.

## Validation

Acceptance criteria evidence:

- Pending dependency and design decisions.

Tests or equivalent validation:

- `nice .agents/sow/audit.sh`: this SOW passes its status/directory,
  regression-placement, open-source-evidence, and sensitive-data checks. The
  unrelated historical SOW-0025 status issue keeps the repository-wide verdict
  partial.
- Placeholder, personal-name, trailing-whitespace, and `git diff --check`
  hygiene scans: pass.
- No product validation is claimed for this tracking-only SOW.

Real-use evidence:

- Pending.

Reviewer findings:

- Pending.

Same-failure scan:

- No existing daemon implementation was found.

Sensitive data gate:

- This SOW contains no credentials, tokens, customer information, private
  endpoints, or proprietary operational details.

Artifact maintenance gate:

- AGENTS.md: unchanged; no daemon exists.
- Runtime project skills: unchanged; no daemon workflow exists.
- Specs: unchanged; design decisions are blocked.
- End-user/operator docs: unchanged; no supported daemon exists.
- End-user/operator skills: none exist.
- SOW lifecycle: open/pending behind SOW-0028.

Specs update:

- Pending approved daemon design.

Project skills update:

- Pending delivered workflow.

End-user/operator docs update:

- Pending delivered behavior.

End-user/operator skills update:

- None currently exist; reassess before close.

Lessons:

- Reusing JSON-RPC payloads does not make a network listener security-neutral.

Follow-up mapping:

- SOW-0028 is the blocking dependency and semantic authority.

## Outcome

Pending.

## Lessons Extracted

Pending implementation and final review.

## Followup

- Complete SOW-0028 before activating this SOW.

## Regression Log

None yet.

Append regression entries here only after completion/closure and a later
regression. Never prepend regression content above the original narrative.
