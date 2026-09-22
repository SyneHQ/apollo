"""Invoked by the companion Go TLS/metadata integration test, never by dispatch."""
import json
from pathlib import Path
import ssl
import sys
import threading
import time
from unittest.mock import patch
from urllib.parse import parse_qs

from syne_connectors.bridge import BridgeSink
from syne_connectors.handoff import write_pages
from syne_connectors.manifest import ConnectorError, validate_manifest
from syne_connectors.pages import rest_pages
from syne_connectors.records import normalize_record
from syne_connectors.transport import BoundedSession, Budget, SourceResponse


def main():
    settings = json.load(sys.stdin)
    budget = Budget(time.monotonic()+30, threading.Event())
    context = ssl.create_default_context(cafile=settings["ca_file"])
    sink = BridgeSink(settings["origin"], settings["token"], budget, tls_context=context)
    sink.install()
    manifest = validate_manifest((Path(__file__).parent/"fixtures/razorpay.json").read_bytes())
    stream = manifest["streams"][0]
    values = {"key_id":"fixture", "key_secret":"fixture", "year":2026, "month":9}
    rows = [{"id":f"settlement-{i}", "amount":123456789012345678+i,
             "fees":17, "tax":0, "created_at":1790020000, "status":"processed"} for i in range(3)]
    def source(_session, request, url):
        offset = int(parse_qs(url.query)["skip"][0])
        response = SourceResponse(); response.status_code=200
        response._content=json.dumps({"items":rows if offset == 0 else []}).encode()
        return response
    class InterruptedSink:
        def commit(self, raw):
            sink.commit(raw)
            raise ConnectorError("simulated_worker_exit")
    normalize = lambda row:normalize_record(manifest,stream,row)
    with patch.object(BoundedSession,"_once",source):
        state=sink.state()
        try:
            write_pages(rest_pages(manifest,stream,values,state["checkpoint"],budget),InterruptedSink(),state,
                        settings["run_id"],normalize,budget,batch_rows=1)
            raise AssertionError("worker did not stop")
        except ConnectorError as error:
            assert str(error)=="simulated_worker_exit"
        resumed=sink.state()
        assert resumed["sequence"]==1 and resumed["checkpoint"]["row_offset"]==1
        result=write_pages(rest_pages(manifest,stream,values,resumed["checkpoint"],budget),sink,resumed,
                           settings["run_id"],normalize,budget,batch_rows=1)
    state=sink.state()
    assert state["checkpoint"]["done"] is True and result["records_committed"]==2
    print(json.dumps({"sequence":state["sequence"],"resumed_records":2,"done":True}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Do not print bootstrap credentials, request objects or exception bodies.
        print("bridge acceptance failed",file=sys.stderr)
        sys.exit(1)
