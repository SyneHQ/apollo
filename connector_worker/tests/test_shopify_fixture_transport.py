"""Actual dlt and Shopify validation over an in-memory HTTP boundary, no sockets."""
from datetime import datetime, timezone
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from shopify_fixture.provider import CONTROL_PATH, SOURCE_PATH, FixtureProvider, MAX_RESPONSE
from shopify_fixture.transport import fixture_once
from syne_connectors.handoff import write_pages
from syne_connectors.manifest import ConnectorError
from syne_connectors.shopify.adapter import shopify_pages
from syne_connectors.shopify.records import normalize_shopify
from syne_connectors.shopify.transport import ShopifySession
from syne_connectors.transport import BoundedSession
from test_pages import Sink, budget
from test_shopify_adapter import MANIFEST
from test_shopify_transport import LIMITS, prepared
from test_shopify_fixture_provider import SOURCE_TOKEN, CONTROL_TOKEN, FILTER


class Reply:
    def __init__(self, status, headers, body):
        self.status, self.headers, self.body = status, headers, io.BytesIO(body)

    def getheader(self, key, default=None): return self.headers.get(key, default)
    def getheaders(self): return list(self.headers.items())
    def read1(self, size): return self.body.read(size)


class ShopifyFixtureTransportTests(unittest.TestCase):
    def setUp(self):
        self.provider = FixtureProvider(SOURCE_TOKEN, CONTROL_TOKEN)
        self.calls, self.connections = [], []
        self.mutate = lambda status, headers, body: (status, headers, body)
        outer = self
        class Connection:
            sock = None
            def __init__(self, host, port, **kwargs):
                outer.assertEqual((host, port), ("127.0.0.1", 3214))
                outer.assertEqual(kwargs["context"], "verified-context")
                self.closed = False
                outer.connections.append(self)
            def request(self, method, path, *, body, headers):
                outer.calls.append((method, path, headers))
                self.reply = Reply(*outer.mutate(*outer.provider.respond(method, path,
                    {key.lower(): value for key, value in headers.items()}, body)))
            def getresponse(self): return self.reply
            def close(self): self.closed = True
        self.environment = patch.dict(os.environ, {"CONNECTOR_BOOTSTRAP_ORIGIN": "https://127.0.0.1:3214",
                                                   "CONNECTOR_SERVICE_CA_PEM": "fixture-CA"})
        self.tls = patch("shopify_fixture.transport.ssl.create_default_context", return_value="verified-context")
        self.network = patch("shopify_fixture.transport.http.client.HTTPSConnection", Connection)
        self.adapter = patch.object(BoundedSession, "_once", fixture_once)
        for context in [self.environment, self.tls, self.network, self.adapter]:
            context.start(); self.addCleanup(context.stop)

    def extract(self, stream_name, sink=None):
        sink, b = sink or Sink(), budget()
        stream = next(row for row in MANIFEST["streams"] if row["id"] == stream_name)
        values = {"store": "merchant", "access_token": SOURCE_TOKEN, "start_date": "2026-09-01", "sync_mode": "full_refresh"}
        pages = shopify_pages(MANIFEST, stream, values, sink.state["checkpoint"], b,
                             now=datetime(2026, 9, 22, 12, tzinfo=timezone.utc))
        try:
            result = write_pages(pages, sink, sink.state, "fixture-run", lambda row: normalize_shopify(MANIFEST, stream, row), b)
        finally:
            pages.close()
        return sink, result

    def test_real_dlt_normalizes_all_cases_and_corrections_without_fabricated_rows(self):
        refunds, _ = self.extract("refunds")
        transactions, _ = self.extract("refund_transactions")
        self.assertEqual((len(refunds.rows), len(transactions.rows)), (4, 4))
        before_refunds, before_transactions = dict(refunds.rows), dict(transactions.rows)
        fields = {row["payload"]["fields"]["id"]: row["payload"]["fields"] for row in transactions.rows.values()}
        self.assertEqual(fields["gid://shopify/OrderTransaction/2"]["status"], "FAILURE")
        self.assertEqual(fields["gid://shopify/OrderTransaction/4"]["currency"], "EUR")
        self.provider.respond("POST", CONTROL_PATH, {"content-type": "application/json", "x-fixture-control": CONTROL_TOKEN}, b'{"mode":"corrected"}')
        refunds, _ = self.extract("refunds", refunds)
        transactions, _ = self.extract("refund_transactions", transactions)
        self.assertEqual(sum(before_refunds[key] != row for key, row in refunds.rows.items()), 1)
        self.assertEqual(sum(before_transactions[key] != row for key, row in transactions.rows.items()), 1)
        corrected = (dict(refunds.rows), dict(transactions.rows))
        refunds, _ = self.extract("refunds", refunds)
        transactions, _ = self.extract("refund_transactions", transactions)
        self.assertEqual(corrected, (refunds.rows, transactions.rows))
        self.assertTrue(all(connection.closed for connection in self.connections))
        self.assertTrue(all(method == "POST" and path == SOURCE_PATH for method, path, _ in self.calls))
        self.assertTrue(all(set(headers) == {"Content-Type", "Accept-Encoding", "X-Shopify-Access-Token"} for _, _, headers in self.calls))

    def test_production_query_and_response_validators_remain_active(self):
        with self.assertRaises(ConnectorError):
            ShopifySession("merchant.myshopify.com", LIMITS, budget()).send(prepared(query="mutation { danger }"))
        self.assertEqual(self.calls, [])
        self.mutate = lambda status, headers, body: (status, {**headers, "X-Shopify-API-Version": "wrong"}, body)
        with self.assertRaisesRegex(ConnectorError, "shopify_api_version_changed"):
            self.extract("refunds")
        self.assertTrue(all(connection.closed for connection in self.connections))

    def test_other_sources_missing_ca_and_invalid_origins_cannot_fall_through(self):
        with self.assertRaisesRegex(ConnectorError, "fixture_source_denied"):
            ShopifySession("other.myshopify.com", LIMITS, budget()).send(prepared(operation="access", variables={}, url="https://other.myshopify.com/admin/api/2026-07/graphql.json"))
        for origin in ["http://127.0.0.1:3214", "https://user:pass@127.0.0.1", "https://127.0.0.1/path", "https://127.0.0.1?x=1", "https://127.0.0.1:bad"]:
            with self.subTest(origin=origin), patch.dict(os.environ, {"CONNECTOR_BOOTSTRAP_ORIGIN": origin}):
                with self.assertRaisesRegex(ConnectorError, "fixture_transport_unconfigured"): self.extract("refunds")
        with patch.dict(os.environ, {"CONNECTOR_SERVICE_CA_PEM": ""}):
            with self.assertRaisesRegex(ConnectorError, "fixture_transport_unconfigured"): self.extract("refunds")
        self.assertEqual(self.calls, [])

    def test_bounded_http_no_redirects_and_truncated_response_denial(self):
        cases = [(lambda s, h, b: (s, {**h, "Content-Length": str(MAX_RESPONSE + 1)}, b), "response_limit"),
                 (lambda s, h, b: (s, {**h, "Content-Length": str(len(b) + 1)}, b), "fixture_response_incomplete"),
                 (lambda s, h, b: (s, {**h, "Content-Encoding": "gzip"}, b), "source_encoding_unsupported"),
                 (lambda s, h, b: (302, {**h, "Location": "https://elsewhere"}, b), "source_http_302")]
        for mutate, code in cases:
            self.mutate = mutate
            with self.subTest(code=code), self.assertRaisesRegex(ConnectorError, code): self.extract("refunds")
        self.assertTrue(all(connection.closed for connection in self.connections))

    def test_missing_fixture_import_terminates_instead_of_running_unpatched_worker(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(__file__).parent / "shopify_fixture" / "sitecustomize.py"
            Path(folder, "sitecustomize.py").write_bytes(source.read_bytes())
            process = subprocess.run([sys.executable, "-c", "print('worker-started')"], cwd=folder,
                env={**os.environ, "PYTHONPATH": folder}, capture_output=True, timeout=10)
            self.assertEqual(process.returncode, 78)
            self.assertNotIn(b"worker-started", process.stdout)
            self.assertIn(b"fixture bootstrap failed", process.stderr)


if __name__ == "__main__":
    unittest.main()
