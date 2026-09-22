"""Companion Go TLS test; bootstrap and object bytes are explicit fixtures."""
import json
import sys
import threading
import time

from syne_connectors.bridge import BridgeSink
from syne_connectors.manifest import ConnectorError
from syne_connectors.transport import Budget
from syne_connectors.worker import run_worker
import ssl


def main():
    env = json.load(sys.stdin)
    context = ssl.create_default_context(cadata=env["CONNECTOR_SERVICE_CA_PEM"])
    sink = BridgeSink(env["CONNECTOR_BRIDGE_ORIGIN"], env["CONNECTOR_INGESTION_TOKEN"],
                      Budget(time.monotonic()+45, threading.Event()), tls_context=context)
    try:
        run_worker(env, threading.Event())
        raise AssertionError("truncated file succeeded")
    except (ConnectorError, ConnectionError):
        assert sink.state()["sequence"] == 0
    first = run_worker(env, threading.Event())
    repeated = run_worker(env, threading.Event())
    assert first["records_committed"] == repeated["records_committed"] == 501
    assert first["sequence"] == 3 and repeated["sequence"] == 6
    print(json.dumps({"done":True,"sequence":6,"truncated_download_rejected":True}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("file acceptance failed", file=sys.stderr)
        sys.exit(1)
