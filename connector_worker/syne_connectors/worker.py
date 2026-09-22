"""One leased REST sync: bootstrap, extract, persist, verify terminal state."""
from datetime import datetime
import json
import os
import re
import signal
import ssl
import sys
import threading
import time
from uuid import UUID

from .bridge import BootstrapClient, BridgeSink
from .handoff import write_pages
from .manifest import ConnectorError, canonical, configuration, require, validate_manifest
from .pages import rest_pages
from .records import normalize_record
from .transport import Budget


def retry_control(action, budget):
    for attempt in range(3):
        budget.remaining()
        try:
            return action()
        except (ConnectionError, TimeoutError):
            require(attempt < 2, "destination_ack_unknown")
            budget.wait(2**attempt)


def run_worker(env, cancelled):
    try:
        run_id = str(UUID(env["CONNECTOR_RUN_ID"]))
        remaining = int(env["CONNECTOR_DEADLINE_EPOCH"]) - time.time()
        require(0 < remaining <= 900, "worker_deadline_invalid")
    except (KeyError, ValueError, TypeError):
        raise ConnectorError("worker_configuration_invalid") from None
    budget = Budget(time.monotonic()+remaining, cancelled)
    ca = env.get("CONNECTOR_SERVICE_CA_PEM")
    require(ca is None or (isinstance(ca, str) and len(ca) <= 65536), "worker_configuration_invalid")
    # Optional operator-supplied CA for private service ingress. Source API trust
    # remains separate; this never disables certificate/hostname verification.
    context = ssl.create_default_context(cadata=ca)
    bootstrap = BootstrapClient(env.get("CONNECTOR_BOOTSTRAP_ORIGIN"), env.get("CONNECTOR_BOOTSTRAP_TOKEN"), budget, tls_context=context)
    data = retry_control(bootstrap.fetch, budget)
    require(set(data) == {"run_id", "stream_id", "manifest_digest", "manifest", "configuration", "expires_at"}
            and data["run_id"] == run_id and isinstance(data["manifest_digest"], str)
            and re.fullmatch(r"[a-f0-9]{64}", data["manifest_digest"]), "worker_bootstrap_invalid")
    try:
        expiry = datetime.fromisoformat(data["expires_at"].replace("Z", "+00:00"))
        require(expiry.utcoffset() is not None, "worker_bootstrap_invalid")
        remaining = expiry.timestamp() - time.time()
        require(0 < remaining <= 900, "worker_deadline_invalid")
    except (TypeError, AttributeError, ValueError, OverflowError):
        raise ConnectorError("worker_bootstrap_invalid") from None
    budget = Budget(min(budget.deadline, time.monotonic()+remaining), cancelled)
    manifest = validate_manifest(canonical(data["manifest"]), data["manifest_digest"])
    require(manifest["runtime"]["kind"] == "rest", "runtime_unsupported")
    streams = [stream for stream in manifest["streams"] if stream["id"] == data["stream_id"]]
    require(len(streams) == 1, "worker_stream_invalid")
    stream = streams[0]
    require(stream["sync"]["mode"] == "snapshot", "incremental_adapter_required")
    require(manifest["id"] != "syne/razorpay" or manifest["version"] == "1.0.1", "connector_upgrade_required")
    values = configuration(manifest, data["configuration"])
    sink = BridgeSink(env.get("CONNECTOR_BRIDGE_ORIGIN"), env.get("CONNECTOR_INGESTION_TOKEN"), budget, tls_context=context)
    retry_control(sink.install, budget)
    state = retry_control(sink.state, budget)
    result = write_pages(rest_pages(manifest, stream, values, state["checkpoint"], budget), sink, state,
                         run_id, lambda row: normalize_record(manifest, stream, row), budget)
    final = retry_control(sink.state, budget)
    require(final["sequence"] == result["sequence"] and final["checkpoint"].get("done") is True,
            "destination_not_complete")
    return {"status": "succeeded", "run_id": run_id, "sequence": final["sequence"],
            "records_committed": result["records_committed"], "done": True}


def main():
    cancelled = threading.Event()
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, lambda *_: cancelled.set())
    try:
        result = run_worker(os.environ, cancelled)
    except Exception:
        # No traceback, request objects, decrypted configuration or source errors.
        print(json.dumps({"status": "failed", "error": "connector_worker_failed"}), file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
