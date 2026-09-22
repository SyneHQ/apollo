# Local fixture image build validation

Validated on the local Docker 29.4.0 engine, linux/arm64, on 2026-09-22.

- Installed base: `sha256:927dbff24d129f41d30582062c2b9122b9ef7ec09ff19a087838609da7f40580`.
- Derived test-only fixture: `sha256:5afac1ff584e245094ea8e75e2cefaccb7c37626063536129d3f04c121582b02`.
- The helper built twice using an inspected temporary local base alias and the explicitly selected legacy builder. Both builds returned the same fixture ID. No package installation, image pull, registry publication or daemon restart was performed.
- Image inspection verified all nine base layers as the exact prefix of eleven fixture layers, the same linux/arm64 platform, inherited `10001:10001` user and command/runtime configuration, and only the intended fixture labels and `PYTHONPATH` setting.
- Both temporary aliases were removed while preserving the original base image/tag. The fixture image remains installed for the separate acceptance harness.
- A short `--pull=never --network=none --read-only` container with dropped capabilities, no new privileges, bounded memory/CPU/PIDs and a bounded temporary filesystem verified UID 10001 and that Python startup installed the fixture `BoundedSession._once` replacement. The container removed itself afterward. It made no provider request and started no server.
- Eight standard-library builder guard tests pass without calling Docker. They cover image ancestry/configuration, local endpoint pinning, reviewed minimal context, mutable-ID/ONBUILD rejection, early evidence-path checks, temporary-tag cleanup, failed-build behavior and preservation of the base's last reference.

This establishes local image construction, configuration and fixture bootstrap only. It does not establish Shopify merchant TLS/DNS, authenticated application APIs, KMS, Apollo dispatch, Go ingestion, refund reconciliation or multi-user report behavior. Those require separate acceptance results. Docker was already running for the parent validation and was left running.
