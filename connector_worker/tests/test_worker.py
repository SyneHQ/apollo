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

from syne_connectors.manifest import ConnectorError, canonical, configuration, content_digest
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


class PrivateWorkerTests(unittest.TestCase):
    fixture = WorkerTests.fixture
    def private_fixture(self):
        from hashlib import sha256
        env, data, page = self.fixture()
        m = data["manifest"]
        m["id"] = "acme/payments"; m["publisher"]["id"] = "acme"
        m["capabilities"].update(preview=False, fullRefresh=True, incremental=False)
        m["limits"]["concurrency"] = 1
        m["runtime"]["origin"] = {"host": "api.acme.example"}
        m["configuration"].append({"key":"region", "label":"Region", "type":"string", "required":False, "secret":False, "default":"us"})
        data["manifest_digest"] = content_digest(m)
        identity = {"id":str(uuid4()), "team_id":"team-one", "manifest_digest":data["manifest_digest"], "approved_origin":"https://api.acme.example"}
        policy = {"version":1, "installationId":identity["id"], "teamId":identity["team_id"], "manifestDigest":identity["manifest_digest"], "approvedOrigin":identity["approved_origin"]}
        identity["policy_digest"] = sha256(b"syne:connector-install-policy:v1\n" + canonical(policy)).hexdigest()
        effective = configuration(m, data["configuration"])
        identity["binding"] = content_digest({"manifestDigest":identity["manifest_digest"], "configuration":{k:v for k,v in effective.items() if k != "key_secret"}, "fileHash":None, "installationId":identity["id"], "policyDigest":identity["policy_digest"]})
        env.update(CONNECTOR_PRIVATE_SYNCS_ENABLED="true", CONNECTOR_PRIVATE_INSTALLATION=json.dumps(identity))
        data["installation"] = {k:identity[k] for k in ["id", "policy_digest", "approved_origin"]}
        page.records[0]["created_at"] = "2026-09-21T00:00:00Z"
        return env, data, page

    def test_private_reviewed_origin_effective_defaults_and_secret_rotation(self):
        env, data, page = self.private_fixture()
        # The snapshot has the effective region default, while bootstrap input can
        # omit it. Secret rotation must not change analytical run identity.
        data["configuration"]["key_secret"] = "rotated-secret-canary"
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink",return_value=Destination()), patch("syne_connectors.worker.rest_pages",return_value=iter([page])):
            bootstrap.return_value.fetch.return_value=data
            self.assertEqual(run_worker(env,threading.Event())["records_committed"],1)

    def test_private_blank_default_request_uses_normalized_snapshot(self):
        env, data, page = self.private_fixture()
        # Private app admission normalizes an explicitly blank optional field
        # before freezing its nonsecret snapshot. Bootstrap returns that value.
        original_request = {**data["configuration"], "region": ""}
        self.assertNotIn("region", configuration(data["manifest"], original_request))
        data["configuration"]["region"] = "us"
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink", return_value=Destination()), patch("syne_connectors.worker.rest_pages", return_value=iter([page])):
            bootstrap.return_value.fetch.return_value = data
            self.assertEqual(run_worker(env, threading.Event())["records_committed"], 1)

    def test_private_unreviewed_scope_config_and_disabled_flags_never_open_destination(self):
        for mutation in ["flag", "missing_trust", "missing_installation", "null_installation", "extra_installation", "id", "policy", "origin", "manifest", "team", "binding", "config", "reserved", "preview", "concurrency", "subdomain", "incremental"]:
            with self.subTest(mutation=mutation):
                env, data, _ = self.private_fixture()
                identity = json.loads(env["CONNECTOR_PRIVATE_INSTALLATION"])
                if mutation == "flag": env["CONNECTOR_PRIVATE_SYNCS_ENABLED"] = "false"
                elif mutation == "missing_trust": del env["CONNECTOR_PRIVATE_INSTALLATION"]
                elif mutation == "missing_installation": del data["installation"]
                elif mutation == "null_installation": data["installation"] = None
                elif mutation == "extra_installation": data["installation"]["config"] = {}
                elif mutation == "id": data["installation"]["id"] = str(uuid4())
                elif mutation == "policy": data["installation"]["policy_digest"] = "a"*64
                elif mutation == "origin": data["manifest"]["runtime"]["origin"]["host"] = "other.example"
                elif mutation == "manifest": identity["manifest_digest"] = "a"*64
                elif mutation == "team": identity["team_id"] = "other-team"
                elif mutation == "binding": identity["binding"] = "a"*64
                elif mutation == "config": data["configuration"]["region"] = "eu"
                elif mutation == "reserved": data["manifest"]["id"] = "syne/payments"; data["manifest"]["publisher"]["id"] = "syne"
                elif mutation == "preview": data["manifest"]["capabilities"]["preview"] = True
                elif mutation == "concurrency": data["manifest"]["limits"]["concurrency"] = 2
                elif mutation == "subdomain": data["manifest"]["runtime"]["origin"]["subdomainField"] = "region"
                elif mutation == "incremental": data["manifest"]["streams"][0]["sync"]["mode"] = "incremental"
                if mutation != "missing_trust": env["CONNECTOR_PRIVATE_INSTALLATION"] = json.dumps(identity)
                data["manifest_digest"] = content_digest(data["manifest"])
                with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink") as destination:
                    bootstrap.return_value.fetch.return_value=data
                    with self.assertRaises(ConnectorError): run_worker(env,threading.Event())
                    destination.assert_not_called()

    def test_builtin_cannot_acquire_private_identity(self):
        env, data, _ = self.fixture()
        data["installation"] = {"id":str(uuid4()), "policy_digest":"a"*64, "approved_origin":"https://api.razorpay.com"}
        with patch("syne_connectors.worker.BootstrapClient") as bootstrap, patch("syne_connectors.worker.BridgeSink") as destination:
            bootstrap.return_value.fetch.return_value=data
            with self.assertRaises(ConnectorError): run_worker(env,threading.Event())
            destination.assert_not_called()
