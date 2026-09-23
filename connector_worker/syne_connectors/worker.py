"""One leased source sync: bootstrap, extract, persist, verify terminal state."""
from contextlib import ExitStack
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
from .csv_source import csv_pages
from .records import normalize_record
from .shopify.adapter import shopify_pages
from .installation import validate_installation
from .shopify.records import normalize_shopify
from .transport import Budget
from .diagnostics import public_failure


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
    require(set(data) - {"file_hash", "installation"} == {"run_id", "stream_id", "manifest_digest", "manifest", "configuration", "expires_at"}
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
    bootstrap.budget = budget
    manifest = validate_manifest(canonical(data["manifest"]), data["manifest_digest"])
    kind = manifest["runtime"]["kind"]
    shopify = (kind == "reviewed_adapter" and manifest["id"] == "syne/shopify"
               and manifest["version"] == "1.1.0" and manifest["runtime"]["adapter"] == "shopify_graphql_v1")
    require(kind in {"rest", "file"} or shopify, "runtime_unsupported")
    if kind == "file":
        require(isinstance(data.get("file_hash"), str) and re.fullmatch(r"[a-f0-9]{64}", data["file_hash"]), "file_hash_required")
    else:
        require("file_hash" not in data, "worker_bootstrap_invalid")
    streams = [stream for stream in manifest["streams"] if stream["id"] == data["stream_id"]]
    require(len(streams) == 1, "worker_stream_invalid")
    stream = streams[0]
    require(shopify or stream["sync"]["mode"] == "snapshot", "incremental_adapter_required")
    require(manifest["id"] != "syne/razorpay" or manifest["version"] == "1.0.1", "connector_upgrade_required")
    values = configuration(manifest, data["configuration"])
    validate_installation(env, data, manifest, values)
    sink = BridgeSink(env.get("CONNECTOR_BRIDGE_ORIGIN"), env.get("CONNECTOR_INGESTION_TOKEN"), budget, tls_context=context)
    retry_control(sink.install, budget)
    state = retry_control(sink.state, budget)
    with ExitStack() as stack:
        if kind == "file":
            source = stack.enter_context(bootstrap.file())
            pages = csv_pages(manifest, values, source, data["file_hash"], state["checkpoint"], budget)
        elif shopify:
            pages = shopify_pages(manifest, stream, values, state["checkpoint"], budget)
        else:
            pages = rest_pages(manifest, stream, values, state["checkpoint"], budget)
        if hasattr(pages, "close"):
            stack.callback(pages.close)
        normalize = normalize_shopify if shopify else normalize_record
        result = write_pages(pages, sink, state, run_id, lambda row: normalize(manifest, stream, row), budget)
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
    except Exception as error:
        # No traceback, request objects, decrypted configuration or source errors.
        try:
            run_id = str(UUID(os.environ.get("CONNECTOR_RUN_ID", "")))
        except ValueError:
            run_id = None
        print(json.dumps({"status": "failed", "run_id": run_id, "error": public_failure(error)}), file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
