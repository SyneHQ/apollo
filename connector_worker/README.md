# Connector worker library

This is the isolated Python ingestion worker foundation for Apollo. It is not yet dispatched by the scheduler and does not receive production credentials. Existing Flowr SQL pipelines remain separate; this worker uses the same job infrastructure for replicated API/file sources.

## Dependencies and contract

The pinned dlt 1.30.0 OSS package (Apache-2.0) provides REST pagination and extraction. `requests` supplies its session interface; our transport enforces a narrower policy. `jsonschema` 4.26.0 (MIT) validates the portable app contract before explicit semantic checks. These dependencies avoid another connector control plane. `requirements.lock` records the complete resolved environment tested with Python 3.12. Install the lock in an isolated environment, then `pip install --no-deps .`.

The schema and three fixtures are copied from SyneHQ/app.ts PR #74 (`4095e0b`). `tests/fixtures/digests.json` was produced with that app's `connectorContentDigest` implementation. Worker tests require identical digests. Update the snapshot and receipts together when the manifest contract changes. Python also rejects control characters, surrogate text and excessive nesting. A manifest is data, never an execution or network grant.

## Network boundary

`BoundedSession` integrates with dlt and permits HTTPS GET only to the exact granted hostname and default TLS port. DNS results must all be public; the verified numeric address is connected directly without a second resolution, while TLS still verifies the original hostname. Redirects, proxies, environment credentials, cookies, arbitrary headers and compressed responses are disabled. Source bodies have byte limits, read timeouts, cancellation/deadline checks and bounded rate-limit retries. An excessive Retry-After defers the job rather than retrying earlier than requested.

The worker's bootstrap must derive the hostname from a separately authorized manifest/configuration and apply infrastructure egress controls as defense in depth. This library does not itself authorize private connector installation. DNS lookup timeout follows the host resolver; it is not a hard real-time deadline.

The dlt client suppresses request logging because DEBUG output can contain credential headers. Missing record selectors, malformed shapes and oversized pages fail instead of becoming empty successful syncs. JSON decimal values parse as Decimal and duplicate keys fail. Provider response bodies and credentials are never included in public errors.

## Validation

Run `PYTHONPATH=. python -m unittest discover -s tests -v` from this directory. Thirteen tests cover cross-language manifest digests, semantic validation, third-manifest compatibility, exact decimals through the installed dlt client, private/mixed DNS denial, numeric-address pinning, host/method restrictions, redirects, retry budgets, cancellation, response caps and connection cleanup. Transport adversarial cases use deterministic sockets/responses; no merchant API access is claimed.

The next increment adds durable page handoff, reviewed adapters and the runner. Go remains the only SQL destination. App grants, scheduler dispatch and end-to-end authenticated acceptance are still required before enabling synchronization.

## Durable REST handoff

The `pages` module compiles only declared offset/page/opaque-cursor pagination to dlt. It never follows provider URLs. Missing cursors and repeated cursors fail; each invocation has a 10,000-page ceiling and a shared deadline. Generic incremental REST streams fail until an adapter defines their ordering semantics; snapshot scans can be resumed or repeated explicitly.

The `handoff` module splits pages by both row and serialized-byte limits. A partial-page checkpoint records the request position, source-page hash and committed row offset. After interruption it refetches that page and rejects changed content before skipping previously committed rows. Fully committed pages store only the next position. Offset pagination on a mutable provider can still shift between pages; repeated snapshots and source-specific reconciliation remain necessary.

Each batch retains its ID, run, timestamp and bytes during ambiguous acknowledgement retries. Progress advances only when the receipt matches ID, sequence, count and SHA-256 digest. The shared `worker-batch` fixture is checked by the actual Go destination validator, including HTML escapes, Unicode and a large decimal. A receipt mismatch or retry exhaustion stops extraction.

Declared fields are checked before handoff. Original source values are retained under `payload.source`, while explicitly typed fields are under `payload.fields`. Razorpay's documented Unix-second timestamps are converted; ordinary REST sources must provide offset-bearing timestamps or a reviewed adapter. Currency values and monetary strings are retained without profit or reconciliation claims.

Twenty-one Python tests now include real dlt pagination over deterministic responses, a third REST manifest through the same extraction logic, lost-ack retries, partial-page resume, changed-page rejection, byte splitting, empty terminal pages and receipt mismatch. Destination tests remain separate; authenticated worker-to-Go dispatch is not enabled yet.

## Merchant CSV pages

`csv_pages` accepts an already authorized binary stream and its granted SHA-256, not a path or URL. It copies at most 50 MB to a private temporary file, verifies the hash, then parses only that immutable copy. UTF-8/BOM, quoted fields and multiline CSV follow Python's strict CSV parser. Headers, mapping, row shape, currency codes, field size and page size are bounded. No formulas are evaluated; their literal text stays in provenance.

Resume uses a verified file plus row position and the same partial-page hash mechanism. Replacing file contents under the same identifier fails the hash check. The execution binding must include this file hash; a file ID alone is insufficient. Empty reports still produce a terminal checkpoint. XLSX remains explicitly unsupported by this first parser.

Twenty-five Python tests pass, adding CSV precision/provenance, formula preservation, file replacement denial, resume, malformed encoding/header/row handling and empty reports. This is parser acceptance, not yet a live workspace file-download or authenticated ingestion claim.

## Razorpay 1.0.1 conformance

The public examples from `https://razorpay.com/docs/api/settlements/fetch-all/` and `https://razorpay.com/docs/api/settlements/fetch-recon/` were checked on 2026-09-22 and are stored as test fixtures. They establish that settlement currency is absent and reconciliation identity uses `entity_id` plus `type`. The corrected manifest retains unknown settlement currency as null and preserves debit, credit, fees, tax and linking IDs. It does not default a country or currency.

Workers require Razorpay 1.0.1; the app retains the archived configuration contract for reopening only. Twenty-seven Python tests now pass, including the provider's settlement/payment/refund/transfer/adjustment examples. Live merchant credentials, history coverage and financial reconciliation remain unverified.

## Scoped Go HTTPS handoff

`BridgeSink` posts exact batch bytes to the three fixed ingestion routes on an
operator-configured HTTPS origin. A scoped job token comes from authorized
bootstrap, never from source configuration. This first-party transport allows
private service addresses; provider extraction retains its separate public-only
network policy. Certificate and hostname verification are mandatory. No proxy,
redirect, cookie, environment credential or arbitrary SQL capability is used.

Responses are capped at 32 KiB; checkpoints at 16 KiB. Revoked grants and sequence
conflicts stop immediately. Transport loss and retryable responses preserve the
exact bytes for the handoff's bounded retry budget. Source records, credentials
and upstream response bodies never appear in public errors.

Thirty-one unit tests pass. The companion Go `TestPythonWorkerThroughTLSBridge`
also runs `tests/bridge_acceptance.py` against an actual TLS handler and disposable
PostgreSQL containing the app run schema. Extraction uses fixture merchant
responses; a committed partial page survives a simulated worker exit and resumes
without duplicate records or versions, preserving a large exact monetary value.
The bootstrap is passed over stdin; the local test CA is explicitly trusted.
This establishes local cross-process persistence and authorization, not live
merchant access, KMS credential resolution, Apollo dispatch or deployment.

## Executable REST worker

`syne-connector-worker` (or `python -m syne_connectors.worker`) runs one leased
snapshot. It retrieves the pinned configuration, validates run/digest/stream and
expiry, initializes the approved destination, resumes its durable checkpoint,
extracts bounded pages and checks the final destination state before emitting a
small terminal JSON result. An incomplete checkpoint cannot produce success.
SIGINT/SIGTERM sets the shared cancellation flag; network reads remain bounded by
their socket deadlines. Error output is a fixed code without tracebacks/secrets.

The operator supplies `CONNECTOR_RUN_ID`, `CONNECTOR_DEADLINE_EPOCH`,
`CONNECTOR_BOOTSTRAP_ORIGIN`, `CONNECTOR_BOOTSTRAP_TOKEN`,
`CONNECTOR_BRIDGE_ORIGIN` and `CONNECTOR_INGESTION_TOKEN`. Bootstrap and ingestion
use separate JWT audiences. Optional `CONNECTOR_SERVICE_CA_PEM` trusts the
operator's private service ingress CA in memory while preserving hostname and
certificate verification. It does not alter source-provider TLS trust.

The Dockerfile pins the Python 3.12 base by digest, installs the complete version
lock and runs as UID/GID 10001. It was built locally for Linux ARM64 and all 35
tests passed inside it with no network, a read-only root filesystem, 64 MiB tmpfs,
all capabilities dropped, no-new-privileges, 512 MiB memory, one CPU and a PID
limit. The image is approximately 202 MB. Unit fixtures do not establish a
merchant sync. The image has not been published; Apollo's supervisor wiring and
authenticated app-to-container acceptance are still subsequent work.

## Dispatched CSV inputs

The executable worker also accepts the built-in merchant CSV manifest. File
bootstrap requires a pinned `file_hash`; the worker POSTs an empty object to the
fixed `/api/internal/connectors/file` route on the configured app origin using
its existing bootstrap capability. It receives no arbitrary URL/path or storage
credentials. Binary responses are bounded to 50 MB, identity encoding, verified
TLS and the shared deadline. A private temporary copy is hash-verified before
record parsing or commits. An interrupted download fails; an explicit retry
resumes the same durable checkpoint. Destination initialization can precede
that download under the previously reviewed install grant.

Apollo now checks the workspace file, storage and storage tenant during claim,
activity polling and successful completion. The Go companion change locks those
rows during each destination operation. Enable the app's additional
`CONNECTOR_FILE_SYNCS_ENABLED` flag only after deploying both companions and a
new immutable worker image; no production settings are changed here.

All 37 Python tests and the race-enabled queue/supervisor tests passed. The Go
`TestFileWorkerThroughTLSBridge` runs `tests/file_acceptance.py` with real TLS and
PostgreSQL, fixture bootstrap/object responses, a truncated download, lost batch
acknowledgement and two complete 501-row scans. The companion bridge's content
transition deduplication retains 501 current rows and 501 versions with exact
money. This does not claim live object-storage/KMS or authenticated Next.js-to-
container acceptance. Earlier validation counts in this document describe the
corresponding historical increments.

## Shopify read-only GraphQL transport

The reviewed Shopify transport uses dlt's session interface and the same bounded
public-only HTTPS transport as REST. POST is allowed only on the configured
merchant's pinned `/admin/api/2026-07/graphql.json` route, with one of six fixed
read documents. Arbitrary GraphQL, mutations, another shop, extra variables and
an API-version fall-forward fail closed. Generic REST remains GET-only.

GraphQL errors with partial data are rejected. `THROTTLED` responses share the
existing retry/deadline budget, using documented query-cost recovery when
available. Errors contain fixed diagnostic codes, never upstream bodies/tokens.
No dependency or scheduler has been added; dlt remains the extraction library
and Apollo remains the orchestrator.

Forty Python tests pass, including the three new transport tests. These use
fixture GraphQL responses and the real request validation/retry code. The fixed
fields were checked against Shopify's 2026-07 documentation, not a live merchant
schema. This PR does not enable Shopify dispatch; scope checking, complete parent
and child pagination, incremental windows and resume acceptance follow separately.

## Shopify pagination and window primitives

`shopify.client` adds a strict dlt body-cursor paginator. It validates pageInfo
before dlt error formatting, follows empty nonterminal pages, rejects cursor
loops and null/missing parents, and includes resumed counts in the 25,000-object
limit. Its scope check rejects insufficient order-history access before extraction.

`shopify.windows` defines durable half-open update windows. A scan pins its upper
UTC timestamp; completion advances a watermark only after all split windows
finish. Dense windows split at 20,000 records and replay the smaller window;
destination content deduplication makes this safe. A still-dense one-second
window fails explicitly. Incremental runs overlap seven days; full refresh scans
all selected history. Returned order dates must satisfy the filter, and malformed
or incompatible checkpoints fail instead of skipping data.

These are adapter primitives, not enabled dispatch. All 48 Python tests passed,
including real dlt iteration over fixture responses, scope checks, continuation
counts, window splitting, overlap, full refresh and invalid checkpoints. Nested
stream mapping and complete worker acceptance remain subsequent increments.
