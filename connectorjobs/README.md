# Durable connector run queue

The app's `connector_sync_runs` is the durable queue. A reviewed administrator
request exists before Apollo can claim it; there is no save-then-submit RPC gap.
This package contains the claim/lifecycle store and an opt-in supervisor that
executes the pinned REST worker through Apollo's existing local Docker runner.

`Claim` locks one eligible queued run with `SKIP LOCKED`, rechecks current source
configuration/revision and workspace/admin/user/tenant/destination access, and
records one 15-minute lease plus audit. Each claim scans at most 100 eligible
candidates, skipping busy run and installation rows while preserving installation-
before-run lock order. A fully busy window yields until the next supervisor poll. It copies no credentials or source data.
Other dispatchers cannot claim it again, including after a process restart.

`Active` supports cancellation polling; the Go ingestion bridge independently
checks every destination request. `Succeed` requires the reported terminal
sequence to match the bridge's persisted receipt and rechecks authorization.
The supervisor must also validate the worker's terminal result before calling it.
`Fail` accepts only fixed public error codes and cannot overwrite cancellation.
`Expire` reconciles at most 100 abandoned leases/queued requests per call and
audits the transition. Expired work is not automatically rerun: a new explicit
request can resume the durable source checkpoint.

The companion app indexes on `(status, createdAt, id)` and `(status, expiresAt)`
support global queue and expiry scans. Workspace admission remains in the app,
with its existing limit of two active runs and one per source stream. The
supervisor allows two concurrent workers per process and bounds database calls.

Run `CONNECTOR_JOBS_TEST_DSN=... go test -race ./connectorjobs` against a disposable
loopback `commerce_validation` database with the app schema. Tests exercise
concurrent claims, restart durability, lease lifetime, changed sources, revoked
admins, absent/mismatched completion receipts, expiry without replay and late
failure after cancellation. Without that variable, database tests are skipped.
This is real metadata database validation, not an executed merchant sync or
deployed service.

## Supervisor configuration

Set `APOLLO_CONNECTORS_ENABLED=true` only after deploying the app run schema and
enabling its scoped bootstrap and the Go bridge ingestion routes. This first
provider requires `JOBS_PROVIDER=local`; asynchronous cloud batch submission is
not treated as job completion. Configure:

- `APOLLO_CONNECTOR_IMAGE`: immutable registry `@sha256:` reference, or local Docker
  `sha256:` image ID. Mutable tags are rejected.
- `APOLLO_JOB_SIGNING_KEY`: the existing shared job signer, at least 32 characters.
- `CONNECTOR_BOOTSTRAP_ORIGIN` and `CONNECTOR_BRIDGE_ORIGIN`: trusted HTTPS origins.
- Optional `CONNECTOR_SERVICE_CA_PEM`: operator CA for private service ingress.

Each worker receives only run identity/deadline, those service origins, and two
15-minute-or-shorter tokens for different audiences. Platform metadata, KMS,
signing and unrelated job secrets are excluded. The worker command/resources are
fixed; the general gRPC job API still rejects connector jobs, so it cannot bypass
persisted admission or substitute arbitrary commands/images.

The runner enforces UID/GID 10001, a read-only root, 64 MiB `/tmp`, dropped
capabilities, no-new-privileges, one CPU, 512 MiB memory and its existing PID cap.
Connector log intake is capped at 64 KiB; the supervisor accepts at most 4 KiB of
terminal JSON and discards arbitrary runner error bodies. No result is successful
without the worker's `done` marker and matching durable bridge sequence.

Authorization is polled every second and rechecked independently by app bootstrap
and the Go bridge. Cancellation or unavailable authorization stops the container.
Shutdown cancels and waits for connector workers; Docker cleanup has a 15-second
deadline. An abrupt host/process loss leaves a visible lease that expires rather
than blindly starting the same job again.

Supervisor/queue tests passed with the race detector against real disposable
PostgreSQL. Existing server/runner tests and command compilation passed. A separate
test launched the built worker image through the actual Apollo runner and verified
UID 10001, read-only root, zero effective capabilities, no-new-privileges, bounded
tmpfs and absence of platform secrets, then removed the container. The full
authenticated app/KMS/merchant workflow and production deployment remain untested.

Worker failures now use a finite, run-bound diagnostic protocol. A failed local
process returns its bounded logs separately from the runner error; only a matching
run ID, exact failure-report shape and allowlisted code are accepted into run
metadata. Unknown output remains `worker_failed`; arbitrary source/log text is
never copied. Late failures cannot overwrite cancellation. The app companion maps
these codes to fixed explanatory copy. Queue/supervisor tests with real metadata
PostgreSQL and actual LocalRunner container failure tests passed with the race
detector. The isolated worker image passed all 59 Python tests; no deployment.


## Private installation authorization

Private runs require `CONNECTOR_PRIVATE_SYNCS_ENABLED=true` in Apollo, in
addition to the app and Go bridge gates. It defaults off. Existing builtins keep
null installation/policy references and their existing binding bytes.

Claim, cancellation polling and success finalization require matching installation
and policy references on source and run, plus an active same-team installation
with matching manifest ID, version, digest and policy. Claim and success acquire
an installation share lock before locking a run, matching app revocation's
installation-before-run lock order. Revocation makes an existing lease inactive;
the supervisor stops its worker and the Go bridge independently rejects further
customer operations. A terminal process result still requires a durable receipt.

The supervisor passes `CONNECTOR_PRIVATE_INSTALLATION` only for private runs.
Its exact fields are `id`, `policy_digest`, `approved_origin`, `manifest_digest`,
`team_id` and `binding`, derived from the persisted authorized run/installation.
The worker receives no package, installer credentials or signing key through
this handoff. Existing scoped JWTs already carry `scope.binding`; no token
version or broad connection grant is added.

Private metadata columns and `connector_installations` must be migrated before
starting this supervisor. DB-gated tests cover default-off admission, private
claim identity, active-lease flag/revocation denial and success denial. Run them
against the disposable migrated metadata fixture before enabling private sync;
unit tests or skipped DB tests do not establish live acceptance.
