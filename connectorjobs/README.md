# Durable connector run queue

The app's `connector_sync_runs` is the durable queue. A reviewed administrator
request exists before Apollo can claim it; there is no save-then-submit RPC gap.
This package contains the claim/lifecycle store and an opt-in supervisor that
executes the pinned REST worker through Apollo's existing local Docker runner.

`Claim` locks one eligible queued run with `SKIP LOCKED`, rechecks current source
configuration/revision and workspace/admin/user/tenant/destination access, and
records one 15-minute lease plus audit. It copies no credentials or source data.
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
