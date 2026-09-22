from copy import deepcopy
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import threading
import time
import unittest
from unittest.mock import patch
from uuid import uuid4

from syne_connectors.manifest import ConnectorError, content_digest
from syne_connectors.pages import Page
from syne_connectors.worker import main, retry_control, run_worker
from test_pages import budget, Sink


class Destination:
    def __init__(self): self.inner=Sink();self.installs=0
    def install(self): self.installs+=1
    def state(self): return self.inner.state
    def commit(self,raw): return self.inner.commit(raw)


class WorkerTests(unittest.TestCase):
    def fixture(self):
        run=str(uuid4());deadline=int(time.time())+600
        env={"CONNECTOR_RUN_ID":run,"CONNECTOR_DEADLINE_EPOCH":str(deadline),
             "CONNECTOR_BOOTSTRAP_ORIGIN":"https://app.example", "CONNECTOR_BOOTSTRAP_TOKEN":"bootstrap-token",
             "CONNECTOR_BRIDGE_ORIGIN":"https://bridge.example", "CONNECTOR_INGESTION_TOKEN":"ingestion-token"}
        manifest=json.loads((Path(__file__).parent/"fixtures/razorpay.json").read_text())
        data={"run_id":run,"stream_id":"settlements","manifest_digest":content_digest(manifest),"manifest":manifest,
              "configuration":{"key_id":"fixture","key_secret":"secret-canary","year":2026,"month":9},
              "expires_at":datetime.fromtimestamp(deadline,tz=timezone.utc).isoformat()}
        row={"id":"s1","amount":123456789012345678,"fees":0,"tax":0,"created_at":1790020000,"status":"processed"}
        page=Page([row],{"position":0,"done":False},{"position":1,"done":True})
        return env,data,page

    def test_terminal_result_requires_verified_state_and_retains_exact_money(self):
        env,data,page=self.fixture();sink=Destination()
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink",return_value=sink) as factory, patch("syne_connectors.worker.rest_pages",return_value=iter([page])):
            bootstrap.return_value.fetch.return_value=data
            result=run_worker(env,threading.Event())
        self.assertEqual(result,{"status":"succeeded","run_id":env["CONNECTOR_RUN_ID"],"sequence":1,"records_committed":1,"done":True})
        self.assertNotIn("secret-canary",json.dumps(result));self.assertEqual(sink.installs,1)
        self.assertEqual(factory.call_args.args[1],"ingestion-token")
        self.assertEqual(sink.inner.rows["s1"]["payload"]["fields"]["amount"],"123456789012345678")
        self.assertIsNone(sink.inner.rows["s1"]["payload"]["fields"]["currency"])

    def test_wrong_run_manifest_stream_or_configuration_never_opens_destination(self):
        env,data,_=self.fixture()
        for change in [{"run_id":str(uuid4())},{"manifest_digest":"b"*64},{"stream_id":"unknown"},
                       {"configuration":{"key_secret":"secret-canary"}}, {"expires_at":"2026-01-01"}]:
            with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink") as factory:
                bootstrap.return_value.fetch.return_value={**deepcopy(data),**change}
                with self.assertRaises(ConnectorError): run_worker(env,threading.Event())
                factory.assert_not_called()

    def test_incomplete_state_does_not_claim_success(self):
        env,data,page=self.fixture();page.after["done"]=False
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink",return_value=Destination()), patch("syne_connectors.worker.rest_pages",return_value=iter([page])):
            bootstrap.return_value.fetch.return_value=data
            with self.assertRaisesRegex(ConnectorError,"destination_not_complete"):run_worker(env,threading.Event())

    def test_bounded_control_retries_and_sanitized_entrypoint_failure(self):
        count=0
        def failed():
            nonlocal count
            count+=1
            raise ConnectionError("secret-canary")
        with patch("syne_connectors.transport.Budget.wait"):
            with self.assertRaisesRegex(ConnectorError,"destination_ack_unknown"):retry_control(failed,budget())
        self.assertEqual(count,3)
        output=io.StringIO()
        with patch("syne_connectors.worker.run_worker",side_effect=ValueError("secret-canary")),patch("syne_connectors.worker.signal.signal"),patch("sys.stderr",output):
            self.assertEqual(main(),1)
        self.assertNotIn("secret-canary",output.getvalue())
        self.assertEqual(json.loads(output.getvalue())["error"],"connector_worker_failed")

    def test_file_worker_verifies_bytes_and_preserves_exact_amounts(self):
        from contextlib import contextmanager
        from hashlib import sha256
        env,data,_=self.fixture()
        data["manifest"]=json.loads((Path(__file__).parent/"fixtures/merchant-report.json").read_text())
        data["manifest_digest"]=content_digest(data["manifest"])
        data["stream_id"]="report_rows"
        data["configuration"]={"file_id":"fixture-file","source_name":"Merchant","timezone":"UTC","id_column":"id","amount_column":"amount","currency_column":"currency"}
        raw=b"id,amount,currency\none,99999999999999.123456,USD\ntwo,-0.010,EUR\n"
        data["file_hash"]=sha256(raw).hexdigest()
        @contextmanager
        def source():
            with io.BytesIO(raw) as stream:yield stream
        sink=Destination()
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap,patch("syne_connectors.worker.BridgeSink",return_value=sink):
            bootstrap.return_value.fetch.return_value=data
            bootstrap.return_value.file.side_effect=source
            result=run_worker(env,threading.Event())
        self.assertEqual(result["records_committed"],2)
        self.assertEqual(sink.inner.rows["one"]["payload"]["fields"]["amount"],"99999999999999.123456")
        self.assertEqual(sink.inner.rows["two"]["payload"]["fields"]["amount"],"-0.010")
        for invalid in [None,"a"*64]:
            bad=deepcopy(data)
            if invalid is None:del bad["file_hash"]
            else:bad["file_hash"]=invalid
            sink=Destination()
            with patch("syne_connectors.worker.BootstrapClient") as bootstrap,patch("syne_connectors.worker.BridgeSink",return_value=sink):
                bootstrap.return_value.fetch.return_value=bad
                bootstrap.return_value.file.side_effect=source
                with self.assertRaises(ConnectorError):run_worker(env,threading.Event())
            self.assertEqual(sink.inner.rows,{})

    def test_reviewed_shopify_worker_uses_nested_adapter_and_rejects_other_adapters(self):
        from test_shopify_adapter import MANIFEST, Merchant, VALUES
        from syne_connectors.transport import BoundedSession
        env, data, _ = self.fixture()
        data.update(manifest=MANIFEST, manifest_digest=content_digest(MANIFEST), stream_id="line_items", configuration=VALUES)
        merchant = Merchant(); merchant.scopes.append("read_all_orders"); sink = Destination()
        def source(_session, request, url): return merchant.send(request, url)
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink", return_value=sink), patch.object(BoundedSession, "_once", source):
            bootstrap.return_value.fetch.return_value = data
            result = run_worker(env, threading.Event())
        self.assertEqual(result["records_committed"], 55); self.assertTrue(result["done"])
        for changes in [{"version": "1.0.0"}, {"id": "syne/unreviewed"}]:
            bad = deepcopy(data); bad["manifest"].update(changes); bad["manifest_digest"] = content_digest(bad["manifest"])
            with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink") as destination:
                bootstrap.return_value.fetch.return_value = bad
                with self.assertRaises(ConnectorError): run_worker(env, threading.Event())
                destination.assert_not_called()
