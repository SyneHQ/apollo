# Durable connector run queue

The app's `connector_sync_runs` is the durable queue. A reviewed administrator
request exists before Apollo can claim it; there is no save-then-submit RPC gap.
This package contains the claim and lifecycle store. The supervisor and isolated
worker entrypoint are subsequent increments, so this change starts no jobs.

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
with its existing limit of two active runs and one per source stream. The future
supervisor must bound its local concurrency and operation contexts too.

Run `CONNECTOR_JOBS_TEST_DSN=... go test -race ./connectorjobs` against a disposable
loopback `commerce_validation` database with the app schema. Tests exercise
concurrent claims, restart durability, lease lifetime, changed sources, revoked
admins, absent/mismatched completion receipts, expiry without replay and late
failure after cancellation. Without that variable, database tests are skipped.
This is real metadata database validation, not an executed merchant sync or
deployed service.
