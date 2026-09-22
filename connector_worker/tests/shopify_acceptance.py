"""Synthetic merchant; actual worker, verified TLS, Go grants and PostgreSQL."""
import json
import ssl
import sys
import threading
import time
from unittest.mock import patch

from syne_connectors.bridge import BridgeSink
from syne_connectors.manifest import ConnectorError
from syne_connectors.transport import BoundedSession, Budget
from syne_connectors.worker import run_worker
from test_shopify_adapter import Merchant, bag


def main():
    env = json.load(sys.stdin)
    merchant = Merchant(); merchant.scopes.append("read_all_orders"); interrupted = False
    def source(_session, request, url):
        nonlocal interrupted
        values = json.loads(request.body)["variables"]
        if values.get("after") == "25" and "id" in values and not interrupted:
            interrupted = True
            raise ConnectorError("fixture_worker_interrupted")
        return merchant.send(request, url)
    sink = BridgeSink(env["CONNECTOR_BRIDGE_ORIGIN"], env["CONNECTOR_INGESTION_TOKEN"],
                      Budget(time.monotonic()+50, threading.Event()),
                      tls_context=ssl.create_default_context(cadata=env["CONNECTOR_SERVICE_CA_PEM"]))
    with patch.object(BoundedSession, "_once", source):
        try:
            run_worker(env, threading.Event())
            raise AssertionError("interruption did not occur")
        except ConnectorError as error:
            assert str(error) == "fixture_worker_interrupted"
            state = sink.state()
            assert state["sequence"] == 1 and state["checkpoint"]["position"]["child"]["after"] == "25"
        merchant.requests.clear()
        resumed = run_worker(env, threading.Event())
        child_requests = [values for name, values in merchant.requests if name == "line_items"]
        assert child_requests[0]["after"] == "25"
        repeated = run_worker(env, threading.Event())
        merchant.lines[merchant.orders[0]["id"]][0]["originalUnitPriceSet"] = bag("2.30")
        corrected = run_worker(env, threading.Event())
        assert resumed["sequence"] == 3 and resumed["records_committed"] == 30
        assert repeated["sequence"] == 6 and repeated["records_committed"] == 55
        assert corrected["sequence"] == 9 and corrected["records_committed"] == 55
    print(json.dumps({"done": True, "sequence": 9, "child_resume_verified": True}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("shopify acceptance failed", file=sys.stderr)
        sys.exit(1)
