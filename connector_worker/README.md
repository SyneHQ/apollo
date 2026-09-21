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
