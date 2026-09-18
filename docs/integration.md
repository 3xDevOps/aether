# Candidate integration

Candidate integration is a server-owned workflow for assembling ordered evidence
submissions, checking a frozen revision, and either landing or proposing that
revision. It is deliberately independent of mission state. The candidate
engine can run today with ordinary evidence packets; it cannot mark a task
`Done` or change mission state. A mission-policy adapter owns the final handoff
to authoritative mission/accepted-submission state.

## Wire surface

The service is reached through the existing authenticated gateway as
`POST /api/v1/<method>`. The authenticated transport supplies the actor; no
`actor`, run owner, mission generation, or permission assertion is accepted in
JSON. See [local-gateway.md](local-gateway.md#candidate-integration-methods) for
transport and status-code behavior.

The method set and parameter/result types are the declarations in
`internal/protocol/integration.go`:

| Method | Parameters | Result |
| --- | --- | --- |
| `integration.prepare` | `IntegrationPrepareParams` | `{ "candidate": Candidate }` |
| `integration.show` | `IntegrationShowParams` | `{ "candidate": Candidate }` |
| `integration.patch` | `IntegrationShowParams` | `{ "patch": string, "truncated": boolean }` |
| `integration.list` | `IntegrationListParams` | `{ "candidates": CandidateSummary[] }` |
| `integration.resolve` | `IntegrationResolveParams` | `{ "candidate": Candidate }` |
| `integration.verify` | `IntegrationVerifyParams` | `{ "candidate": Candidate }` |
| `integration.request_delivery` | `IntegrationRequestDeliveryParams` | `{ "candidate": Candidate }` |
| `integration.decide` | `IntegrationDecideParams` | `{ "candidate": Candidate }` |
| `integration.deliver` | `IntegrationDeliverParams` | `{ "candidate": Candidate }` |
| `integration.delete` | `IntegrationDeleteParams` | `{}` |

The internal service methods receive the authenticated `integration.Actor` and
return the protocol records used by the transport:

```go
func New(cfg Config) (*Service, error)
func (s *Service) Close()
func (s *Service) Prepare(ctx context.Context, actor Actor, p protocol.IntegrationPrepareParams) (protocol.Candidate, error)
func (s *Service) Show(ctx context.Context, actor Actor, p protocol.IntegrationShowParams) (protocol.Candidate, error)
func (s *Service) Patch(ctx context.Context, actor Actor, p protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error)
func (s *Service) List(ctx context.Context, actor Actor, p protocol.IntegrationListParams) (protocol.IntegrationListResult, error)
func (s *Service) Resolve(ctx context.Context, actor Actor, p protocol.IntegrationResolveParams) (protocol.Candidate, error)
func (s *Service) Verify(ctx context.Context, actor Actor, p protocol.IntegrationVerifyParams) (protocol.Candidate, error)
func (s *Service) RequestDelivery(ctx context.Context, actor Actor, p protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error)
func (s *Service) Decide(ctx context.Context, actor Actor, p protocol.IntegrationDecideParams) (protocol.Candidate, error)
func (s *Service) Deliver(ctx context.Context, actor Actor, p protocol.IntegrationDeliverParams) (protocol.Candidate, error)
func (s *Service) Delete(ctx context.Context, actor Actor, p protocol.IntegrationDeleteParams) error
```

Each call sends only its params object as JSON. For example, these are the
complete request shapes (the gateway path selects the method):

```http
POST /api/v1/integration.prepare
{"workspace_id":"ws_123","submissions":[{"workspace_id":"ws_123","run_id":"run_456","evidence_ref":"evidence_789","retained_revision":"0123456789abcdef0123456789abcdef01234567"}],"target_ref":"refs/heads/main","expected_target_revision":"fedcba9876543210fedcba9876543210fedcba98","required_sources":["git","transcript"],"idempotency_key":"prepare-1"}

POST /api/v1/integration.show
{"workspace_id":"ws_123","candidate_id":"cand_1"}

POST /api/v1/integration.patch
{"workspace_id":"ws_123","candidate_id":"cand_1"}

POST /api/v1/integration.list
{"workspace_id":"ws_123","limit":50}

POST /api/v1/integration.resolve
{"workspace_id":"ws_123","candidate_id":"cand_1","files":[{"path":"src/auth.go","content":"resolved content"}],"idempotency_key":"resolve-1"}

POST /api/v1/integration.verify
{"workspace_id":"ws_123","candidate_id":"cand_1","candidate_revision":"0123456789abcdef0123456789abcdef01234567","argv":["go","test","./..."],"timeout_seconds":300,"idempotency_key":"verify-1"}

POST /api/v1/integration.request_delivery
{"workspace_id":"ws_123","candidate_id":"cand_1","candidate_revision":"0123456789abcdef0123456789abcdef01234567","verification_ids":["ver_1"],"action":"proposal","idempotency_key":"request-1"}

POST /api/v1/integration.decide
{"workspace_id":"ws_123","candidate_id":"cand_1","request_id":"req_1","request_version":1,"approve":true}

POST /api/v1/integration.deliver
{"workspace_id":"ws_123","candidate_id":"cand_1","request_id":"req_1","request_version":1}

POST /api/v1/integration.delete
{"workspace_id":"ws_123","candidate_id":"cand_1"}
```

Success for each aggregate operation except `integration.patch`,
`integration.list`, and `integration.delete` is a JSON object containing the
current aggregate, for example
`{"candidate":{"candidate_id":"cand_1","state":"frozen","candidate_revision":"<oid>",...}}`.
The list response is `{"candidates":[...]}` of bounded `CandidateSummary`
records; each contains `candidate_id`, `workspace_id`, `state`,
`candidate_revision`, `target_ref`, `expected_target_revision`,
`delivery_request`, `delivery_receipt`, `created_at`, and `expires_at`. It
omits source snapshots, verification output, and mutation history.
`integration.show` returns the complete `Candidate`; the patch response is
`{"patch":"...","truncated":false}`. The verify/request/decide/deliver results
also include their newly changed verification, request, or receipt inside that
candidate aggregate. Delete returns `{}` after its durable deletion fence is
recorded.

`integration.list` bounds `limit` to 100 (default 50) and returns only the
bounded summary projection. All IDs, revisions, and idempotency keys are
server-checked.

`integration.patch` is a View-authorized read for the combined-code review.
It requires a frozen candidate and valid candidate-owned refs, rechecks current
workspace/member authorization and the read admission seam, and renders the
exact diff from `expected_target_revision` to the frozen `candidate_revision`.
The result is capped at 1 MiB; `truncated` is true only when the bounded render
ends before the complete diff. It does not mutate the candidate or imply
verification, acceptance, or landing.

## Candidate identity and assembly

`SubmissionRef` is exactly the ordered four-tuple below:

```json
{
  "workspace_id": "ws_123",
  "run_id": "run_456",
  "evidence_ref": "evidence_789",
  "retained_revision": "0123456789abcdef0123456789abcdef01234567"
}
```

`workspace_id`, `run_id`, and `evidence_ref` identify the source packet;

`evidence_ref` is the public durable evidence-packet database ID. It is not
the private Git capture-ref suffix: the service resolves that capture key from
the authoritative packet's run, origin, and idempotency metadata while holding
the evidence lock. Clients never construct or submit the private capture key.
`retained_revision` is the complete source revision retained by the
candidate-owned Git ref. The service validates every tuple, in the submitted
order, against the authenticated workspace and durable evidence packet. It
rejects a packet from another workspace/run, a revision that is not the
packet's retained revision, and any missing, expired, unavailable, or truncated
source required by `required_sources`. The mission-policy adapter must
additionally reject duplicate or reordered identities against its authoritative
accepted submission list.
A requested source is required, not advisory. The default source policy
requires Git; the prepare call can request additional named sources.

Preparation first persists a `preparing` aggregate, then copies each source
under the evidence service's candidate-source lock. Candidate-owned Git refs
and transcript artifacts are independently retained, checksummed, and named
from the server-generated candidate ID and ordered index. Later validation
uses those owned refs/artifacts rather than the original packet row or packet
ID, so an original packet may expire or be purged after transfer without
silently changing the candidate. Missing owned evidence is visible in `show`
as `unavailable`; delivery never trusts metadata alone.

The `Candidate` aggregate carries `candidate_id`, `workspace_id`, optional
`mission_id`, ordered `submissions`, `required_sources`, immutable `inputs`,
`target_ref`, `expected_target_revision`, optional `candidate_revision`,
`state`, optional `conflicts`, `applied_inputs`, `verifications`, optional
`delivery_request`/`delivery_receipt`, `created_at`, `expires_at`, optional
`error`, optimistic `version`, and bounded `mutations`. Each `CandidateInput`
contains its `submission`, `base_revision`, an `EvidencePacket` snapshot, and
optional candidate-relative `transcript_id`/`transcript_checksum`; combined
marshaled packet snapshots are capped at 1 MiB per candidate and owned
transcripts at 16 MiB. Host filesystem paths are never wire fields. Each
mutation has `actor_key`, `operation`, `idempotency_key`, `digest`, and
`result_id`; at most 128 are retained. Aggregate payloads are capped at 16 MiB.
Time values are RFC3339 JSON timestamps.

The packet snapshot and its `sources` are observations and provenance, not an
acceptance decision or semantic proof that the task is complete.

A candidate has a 30-day lifetime and these states:

- `preparing`: ordered inputs are being retained/assembled;
- `conflicted`: assembly stopped with `conflicts`, a deterministic list of
  repository-relative paths;
- `frozen`: every input is applied and `candidate_revision` is immutable;
- `unavailable`: an owned input or required source cannot be validated;
- `deleting`: deletion has fenced all further actions;
- `expired`: the lifetime elapsed (metadata is retained only for bounded
  cleanup/tombstone retention).

`integration.resolve` accepts up to 32 ordered `files` entries of
`{path, content, delete}`. `content` replaces a whole file, including an
explicitly empty string; `delete: true` removes it. Omitted paths are untouched.
Paths must be repository-relative and path-safe. Traversal, symlink traversal,
and unresolved conflict-marker text are rejected. Remaining index conflicts
keep the candidate `conflicted`, so files can be resolved in separate batches.

The service records a resolution intent before changing Git. After an uncertain
error, retry the same idempotency key and payload; a different resolution is
blocked until the pending operation is reconciled. Resolutions continue the
journaled assembly. A conflict-free
candidate becomes `frozen`; a frozen candidate is never edited. The frozen
revision, target ref, and expected target revision are carried into every
verification and delivery request, so stale UI state cannot be adapted into a
new action.

## Verification

`integration.verify` requires `workspace_id`, `candidate_id`, the exact
`candidate_revision`, an `argv` array, `timeout_seconds`, and an
`idempotency_key`. The image, mounts, environment, and runtime configuration
are selected by the server's existing environment builder; clients cannot
supply a host command, arbitrary mount, image, or configuration. `argv` is
bounded to 64 entries and each argument to 16 KiB. Timeout is bounded to
30 minutes (default five minutes), and the gateway control call returns within
its normal 60-second control budget.

The call durably appends a `running` verification and returns immediately with
`{ "candidate": ... }`. A worker then creates/starts/waits on the
server-owned runtime, drains output, and stops/destroys all runtime processes.
Only after destruction does it check the candidate-owned source tree and owned
evidence, then atomically settle the aggregate. It retains at most 64 KiB of
output per verification; excess output is drained and discarded and sets
`output_truncated`. A timeout, cancellation, runtime failure, or source
mutation never becomes `passed`. A changed tracked source, including ignored
tracked paths or mode/deletion changes, is `source_changed`; it does not create
a new candidate revision.

An observed exit code of `0` is the command's recorded result, not semantic
proof that the candidate is correct. The verification status and retained
output/provenance remain the evidence shown to the human.

Each `Verification` reports `verification_id`, `candidate_revision`, `argv`,
server-selected `image`, observed `image`, `user`, `working_dir`,
`timeout_seconds`, `cpu_limit`, `memory_limit_bytes`, `environment_sha256`,
`setup_script_sha256`, `status`, nullable `exit_code`, bounded `output`,
`output_truncated`, `error`, `created_at`, optional `finished_at`, and
`expires_at`. `creation_key` and `container_id` are server-generated opaque
recovery identifiers: they are operational metadata, never client authority or
secret values, and clients cannot select them. Status values are `running`,
`passed`, `failed`, `timed_out`, `cancelled`, `error`, and `source_changed`. A
passed result is valid for 24 hours, never past candidate expiry. Delivery
requires non-empty, duplicate-free verification IDs, exact candidate revision
matches, all selected results currently passed and unexpired, and no later
failed/running attempt overriding the selected pass. The UI must show argv,
output/truncation, status, and observed configuration provenance; an absent or
stale result is missing evidence, not approval.

## Request, human decision, and delivery

`integration.request_delivery` binds a candidate revision to selected
verification IDs, an action, and an idempotency key. The only actions are:

- `update_ref`: local atomic update of the full `refs/heads/<branch>` target;
- `proposal`: mirrored-workspace proposal, never a mirror-base update.

The resulting `DeliveryRequest` contains `request_id`, `request_version`, exact
`candidate_revision`, `verification_ids`, `target_ref`,
`expected_target_revision`, `action`, `state`, `requested_by`, optional
`decided_by`, `created_at`, `expires_at`, and optional `decided_at`. Requests
expire no later than the candidate and selected verification results.
`request_version` is the immutable optimistic-concurrency token for this
reviewed request: the human decision and subsequent delivery use the same
exact value. Replacing or changing a request is not an adaptation; it creates
a new request/version and invalidates the old one.

`integration.decide` is human-only: its actor must have no `RunID`. It takes
`request_id`, the exact `request_version`, and `approve`; it is the human
approval boundary and uses optimistic version fencing. Agent actors cannot
approve or deny. This integration decision is separate from the existing
`approval.decide` flow; that flow's contract is unchanged. `integration.deliver`
takes the exact request/version and
rechecks current membership, push permission, candidate ownership, all
selected evidence, verification validity, approval, and target revision before
claiming `delivering`. A request/version, target, candidate revision, action,
or verification mismatch is rejected rather than rewritten.

Delivery writes a durable Git transaction receipt before the final aggregate
save. If the response or database save is lost, retrying the same request
reconciles that private receipt and returns the original result; it never
infers success merely because the target currently equals the candidate
revision. A `DeliveryReceipt` contains `receipt_id`, `request_id`,
`candidate_revision`, `target_ref`, `previous_revision`, `action`, `result`,
optional `proposal_ref`, and `created_at`. Replaying a receipt still requires
current read/authorization checks.

For `update_ref`, the server atomically updates the local target only when its
value is `expected_target_revision`. For `proposal`, it creates the public
`refs/heads/aether/proposal-<requestID>` ref and private receipt transaction,
leaving the mirror base untouched. The receipt result is `proposed`, not
`landed`; a human can fetch the proposal and push it through the ordinary
upstream protected route. Aether does not fake an upstream push or provision
upstream credentials. Native credential pushes performed outside Aether are
not blocked by this service.

For a proposal, the human workflow remains ordinary Git. Replace the remote
names with the workspace's configured Aether and upstream remotes:

```sh
git fetch aether \
  refs/heads/aether/proposal-<request_id>:refs/remotes/aether/proposal-<request_id>
git log --oneline refs/remotes/aether/proposal-<request_id>
git push upstream \
  refs/remotes/aether/proposal-<request_id>:refs/heads/<protected-branch>
```

The last command is a normal upstream push and may require the upstream's
review/protection flow; it is not performed by `integration.deliver`.

## Admission and mission-policy integration

The internal service is constructed with:

```go
type Config struct {
    Store       Store
    Git         Git
    Evidence    EvidenceSource
    Runtime     runtime.Runtime
    Root        string
    Environment func(context.Context, Actor, *domain.Workspace, string) (runtime.Spec, error)
    Admission   AdmissionFunc
    Now         func() time.Time
}

type Actor struct {
    MemberID domain.MemberID
    RunID    domain.RunID
}

type Admission struct {
    Operation   string
    Actor       Actor
    WorkspaceID domain.WorkspaceID
    MissionID   string
    Candidate   *protocol.Candidate
    Submissions []protocol.SubmissionRef
}

type AdmissionFunc func(context.Context, Admission) (release func(), err error)
```

`AdmissionFunc` is server-controlled and is invoked for **every mutation**,
including `integration.prepare` when `mission_id` is omitted. The default
policy rejects run actors and any non-empty mission ID. The callback must hold
its returned release fence until the consequential operation completes. The
authenticated transport supplies `Actor`: human SSH/gateway calls get only
`MemberID`; the server's run socket adapter is the sole source of `RunID`.
Clients cannot set either field.

The mission-policy adapter is the authoritative integrator. It must resolve the
current mission association, accepted-submission membership, and generation
fence for the caller and every input, including when the request's
`mission_id` is empty. It must reject mission-associated inputs/callers used
through an empty mission ID, stale generations, and unaccepted submissions.
It must not import mission state into this engine or trust client-supplied
mission IDs, candidate IDs, generations, or acceptance claims. The engine's
independent workspace/member/push/read checks remain mandatory.

## Retention and recovery

Candidate, request, and verification lifetimes are bounded as above. Delete
and expiry first persist the fence (`unavailable`/`deleting`) before destroying
verification runtimes and removing owned checkouts, refs, transcripts, and
other artifacts. Recovery scans durable records, marks interrupted
verifications as `error`, destroys containers found by their persisted creation
key, removes disposable checkouts, and never reruns a command or invents an
exit code. A failed cleanup remains recoverable. Candidate tombstone metadata
is retained for at most 30 days; public proposal refs are never removed as
candidate-private artifacts.

Release B appends migration **32** after the v31 schema in
`internal/store/migrate.go`; migrations are append-only and shipped entries
must not be edited. Operators should inspect `schema_migrations` and expect
version 32 rather than infer schema from a client build.
