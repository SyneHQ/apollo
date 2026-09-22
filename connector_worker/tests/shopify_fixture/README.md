# Synthetic Shopify refund provider

This fixture joins the existing authenticated app/Apollo/Go acceptance harness.
It is not a production connector, a live merchant test, or bank confirmation.
Nothing here inserts run receipts, ingestion records, analytical results or
successful run states. The host harness must admit runs through the app and let
the real supervisor, worker and Go bridge produce those records.

## Boundary

The separate fixture image replaces only `BoundedSession._once`. Requests still
pass production `BoundedSession.send`, `ShopifySession.validate_method`, and
Shopify's API-version/GraphQL response validation. Real dlt pagination,
normalization, bootstrap, handoff and Go destination clients remain unchanged.
The replacement denies every source except the exact synthetic Shopify host and
fixed API path; it never falls through to public DNS. A fixture import failure
terminates Python rather than silently running an unpatched worker.

The source network call is forwarded to the fixed
`/_fixture/shopify/graphql` path on `CONNECTOR_BOOTSTRAP_ORIGIN`, over verified
TLS using `CONNECTOR_SERVICE_CA_PEM`. Only the synthetic Shopify token and normal
JSON request are forwarded, never bootstrap or ingestion grants. Responses are
capped at 64 KiB and respect the production byte/time budgets. Redirects and
compressed responses are rejected. The source hostname is retained on the
response for dlt, but **public Shopify DNS, address pinning and merchant TLS are
substituted**. Run the separate production transport tests; do not claim this
fixture validates those network boundaries.

## Host fixture and gateway

Run from `connector_worker` using an installed Python runtime:

```sh
PYTHONPATH=.:tests python -m shopify_fixture.server \
  --port 3218 \
  --source-token-file /absolute/private-task/shopify-source-token \
  --control-token-file /absolute/private-task/shopify-control-token
```

Both files must be regular, nonsymlink, owner-only files containing distinct
32–128 character `[A-Za-z0-9_-]` tokens. The server binds only `127.0.0.1:3218`
and logs no requests. It admits at most four connections with a ten-second
request deadline, 32 KiB request bodies, fixed response data and a 10,000-query
budget. The source-token file's value is the synthetic source `access_token`.
Do not pass real merchant credentials.

The acceptance harness's HTTPS gateway must forward only POST requests on
`/_fixture/shopify/graphql` to this server. It must not expose the control path.
Existing production bootstrap/ingestion proxy routes stay unchanged. The host
can change the fixture stage directly on loopback with POST
`/_fixture/shopify/mode`, `Content-Type: application/json`,
`X-Fixture-Control: <control token>`, and `{"mode":"corrected"}` or
`{"mode":"baseline"}`. Change stages only between completed stream runs.
Each request uses an atomic stage snapshot; this does not provide a consistent
multi-run merchant snapshot.

## Separate immutable image

Use an explicitly supplied, already installed base worker image ID with a
pre-existing persistent local tag. Build from `connector_worker` with installed
Python and Docker; the helper installs nothing:

```sh
python tests/shopify_fixture/build_image.py \
  --base-image sha256:REPLACE_WITH_LOCAL_WORKER_IMAGE_ID \
  --evidence-file /absolute/private-task/new-build-evidence.json
```

A raw Docker image ID cannot be used as a `FROM` name: BuildKit interprets
`sha256:...` as a registry image reference. The helper inspects the supplied ID,
creates a unique task-only local alias, and explicitly selects the legacy Docker
builder (`DOCKER_BUILDKIT=0`) against the selected local Unix-socket engine. If
that builder is unavailable, it fails without trying BuildKit, pulling images,
or downloading a Dockerfile frontend. The base must not contain `ONBUILD`
instructions. Its existing tag ensures cleanup cannot remove its last image
reference, including after a failed build.

`--pull=false` plus the inspected local alias and legacy builder avoid registry
resolution. `--network=none` restricts `RUN` networking only; it does **not** make
BuildKit metadata resolution offline. This fixture's reviewed Dockerfile has no
`RUN`, `ADD`, package installation, or remote frontend. The helper rejects changes
to that instruction contract until explicitly reviewed. Its temporary build
context contains only the Dockerfile and five fixture Python files, excluding
repository configuration, credentials, caches and build logs.

After building, the helper verifies the base identity again, the exact base
layer prefix, platform, inherited non-root user and runtime configuration, and
only the intended fixture labels/PYTHONPATH change. It then removes only its own
base alias, preserves the installed base, and prints the resulting immutable
fixture ID and evidence. If another process removes all original base tags, it
fails and retains its alias rather than risking deletion of the base. It never
removes the resulting fixture image or another process's tag.

Pass the resulting local immutable image ID to the harness/Apollo, not a tag.
Apollo continues to invoke `python -m syne_connectors.worker`; its existing
read-only root, tmpfs, CPU/memory/PID and capability restrictions still apply.
Never publish or deploy this image as a normal connector worker.

## Authenticated source and expected cases

Create `syne/shopify@1.1.0` through the real app source API with:

```json
{"store":"merchant","access_token":"<synthetic source token>","start_date":"2026-09-01","sync_mode":"full_refresh"}
```

The app requires KMS encryption even in development for this token. Provide the
real KMS or a separately disclosed authenticated KMS fixture; do not seed an
encrypted blob or weaken `requireEncryption`.

Admit `refunds` and `refund_transactions` sequentially through the real run API.
Each stream contains four records under one order. The default stage is
`baseline`; query the UTC period 2026-09-01 through 2026-10-01.

| Refund suffix | Recorded USD | Transaction | Baseline result | Corrected result |
| --- | --- | --- | --- | --- |
| 1 | 12.30 | SUCCESS, USD 12.30 | matched | matched |
| 2 | 7.00 | FAILURE, USD 7.00 | unconfirmed_transactions | SUCCESS, matched |
| 3 | 5.00, then 4.00 | SUCCESS, USD 4.00 | amount_mismatch | matched |
| 4 | 2.00 | SUCCESS, EUR 2.00 | currency_mismatch | currency_mismatch |

IDs use `gid://shopify/Refund/1` through `/4`; transaction IDs use
`gid://shopify/OrderTransaction/1` through `/4`. MoneyBags contain decimal strings
and explicit currencies. Baseline reconciliation should have one matched
refund and three exceptions; corrected reconciliation should have three
matched and one currency exception. No cross-currency total is valid.

Both stages deliberately retain the same parent/refund update timestamps. This
exercises the documented full-refresh path for corrections that do not advance
parent timestamps. Exactly one refund record and one transaction record change.
The eight current records should remain eight; the first corrected scan should
retain ten content versions, and subsequent unchanged scans should add none.
Old run-pinned reconciliation must still reproduce the baseline result.

## Verification scope

```sh
PYTHONPATH=.:tests python -m unittest \
  tests/test_shopify_fixture_provider.py tests/test_shopify_fixture_transport.py -v
```

These tests start no server or container. They exercise provider authorization,
malformed input, stage changes, private token files, production Shopify
validators, real dlt extraction/normalization, an in-memory HTTP boundary,
bounded responses and bootstrap failure. They do not establish live gateway
TLS, Apollo dispatch, app credential handling, PostgreSQL persistence or app
reconciliation. Those require a completed host acceptance run with this image.

The complete 70-test worker suite passed using the existing local worker image,
network disabled, a read-only source mount/root filesystem and bounded tmpfs,
CPU, memory and PIDs. The fixture image was subsequently built and smoke-tested locally using the
offline helper; see `BUILD_VALIDATION.md` for the exact immutable IDs and scope.
Gateway and database acceptance are separate from this image validation.
