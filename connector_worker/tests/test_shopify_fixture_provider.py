"""Pure fixture boundary tests; no listeners, containers or merchant access."""
import json
import io
from email.message import Message
from pathlib import Path
import tempfile
import unittest

from shopify_fixture.provider import CONTROL_PATH, SOURCE_PATH, MAX_BODY, FixtureError, FixtureProvider
from shopify_fixture.server import Handler, token_file
from syne_connectors.shopify.queries import API_VERSION, QUERIES

SOURCE_TOKEN, CONTROL_TOKEN = "s" * 48, "c" * 48
FILTER = "created_at:>='2026-09-01T00:00:00Z' updated_at:>='2026-09-01T00:00:00+00:00' updated_at:<'2026-10-01T00:00:00+00:00'"


def request(operation, variables):
    return json.dumps({"query": QUERIES[operation], "variables": variables}).encode()


class ShopifyFixtureProviderTests(unittest.TestCase):
    def setUp(self):
        self.provider = FixtureProvider(SOURCE_TOKEN, CONTROL_TOKEN)
        self.headers = {"content-type": "application/json", "x-shopify-access-token": SOURCE_TOKEN}

    def query(self, operation, variables):
        status, headers, body = self.provider.respond("POST", SOURCE_PATH, self.headers, request(operation, variables))
        self.assertEqual(status, 200)
        self.assertEqual(headers["X-Shopify-API-Version"], API_VERSION)
        self.assertEqual(int(headers["Content-Length"]), len(body))
        return json.loads(body)["data"]

    def mode(self, mode):
        return self.provider.respond("POST", CONTROL_PATH,
            {"content-type": "application/json", "x-fixture-control": CONTROL_TOKEN}, json.dumps({"mode": mode}).encode())

    def test_four_refunds_and_corrected_money_keep_source_identity(self):
        variables = {"first": 25, "after": None, "filter": FILTER}
        before = self.query("refunds", variables)["orders"]["nodes"][0]
        first = self.query("refund_transactions", {"first": 25, "after": None, "id": "gid://shopify/Refund/2"})["refund"]
        self.assertEqual(first["transactions"]["nodes"][0]["status"], "FAILURE")
        self.assertEqual([r["totalRefundedSet"]["shopMoney"]["amount"] for r in before["refunds"]], ["12.30", "7.00", "5.00", "2.00"])
        self.mode("corrected")
        after = self.query("refunds", variables)["orders"]["nodes"][0]
        self.assertEqual([r["id"] for r in before["refunds"]], [r["id"] for r in after["refunds"]])
        self.assertEqual(after["refunds"][2]["totalRefundedSet"]["shopMoney"]["amount"], "4.00")
        second = self.query("refund_transactions", {"first": 25, "after": None, "id": "gid://shopify/Refund/2"})["refund"]
        self.assertEqual(second["transactions"]["nodes"][0]["status"], "SUCCESS")
        foreign = self.query("refund_transactions", {"first": 25, "after": None, "id": "gid://shopify/Refund/4"})["refund"]
        self.assertEqual(foreign["transactions"]["nodes"][0]["amountSet"]["shopMoney"]["currencyCode"], "EUR")
        self.assertEqual(after, self.query("refunds", variables)["orders"]["nodes"][0])

    def test_exact_capabilities_and_paths_reject_untrusted_requests(self):
        body = request("access", {})
        for method, path, headers in [("GET", SOURCE_PATH, self.headers), ("POST", SOURCE_PATH + "?x=1", self.headers),
                ("POST", SOURCE_PATH, {**self.headers, "x-shopify-access-token": CONTROL_TOKEN}),
                ("POST", CONTROL_PATH, {**self.headers, "x-fixture-control": SOURCE_TOKEN}),
                ("POST", SOURCE_PATH, {**self.headers, "content-type": "text/plain"})]:
            with self.subTest(path=path), self.assertRaises(FixtureError):
                self.provider.respond(method, path, headers, body)
        for source, control in [(SOURCE_TOKEN, SOURCE_TOKEN), ("short", CONTROL_TOKEN), (SOURCE_TOKEN, "bad\n" * 16)]:
            with self.assertRaises(FixtureError):
                FixtureProvider(source, control)

    def test_only_fixed_financial_queries_and_bounded_values(self):
        bad = [b'{"query":1,"query":2,"variables":{}}', b"[", b"[" * 2000,
            b"x" * (MAX_BODY + 1), b'{"query":NaN}', request("orders", {"first": 25, "after": None, "filter": FILTER}),
            json.dumps({"query": "mutation { danger }", "variables": {}}).encode(),
            request("refunds", {"first": True, "after": None, "filter": FILTER}),
            request("refunds", {"first": 101, "after": None, "filter": FILTER}),
            request("refunds", {"first": 25, "after": "https://elsewhere", "filter": FILTER}),
            request("refunds", {"first": 25, "after": "9999", "filter": FILTER}),
            request("refunds", {"first": 25, "after": None, "filter": "updated_at:>='2026-10-01' updated_at:<'2026-09-01'"}),
            request("refund_transactions", {"first": 25, "after": None, "id": "gid://shopify/Refund/99"})]
        for raw in bad:
            with self.subTest(size=len(raw)), self.assertRaises(FixtureError):
                self.provider.respond("POST", SOURCE_PATH, self.headers, raw)
        with self.assertRaises(FixtureError):
            self.mode({"invalid": True})

    def test_filter_and_cursor_do_not_invent_records_and_response_is_independent(self):
        variables = {"first": 1, "after": None, "filter": FILTER}
        first = self.query("refunds", variables)["orders"]
        first["nodes"][0]["refunds"].clear()
        self.assertEqual(len(self.query("refunds", variables)["orders"]["nodes"][0]["refunds"]), 4)
        self.assertEqual(self.query("refunds", {**variables, "after": "1"})["orders"]["nodes"], [])
        self.assertEqual(self.query("refunds", {**variables, "filter": FILTER.replace("2026-", "2025-")})["orders"]["nodes"], [])
        self.provider.requests = 10000
        with self.assertRaises(FixtureError) as error:
            self.query("access", {})
        self.assertEqual(error.exception.status, 429)

    def test_token_file_rejects_shared_permissions_symlinks_and_oversize(self):
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / "token"
            file.write_text(SOURCE_TOKEN)
            file.chmod(0o600)
            self.assertEqual(token_file(str(file)), SOURCE_TOKEN)
            link = Path(folder) / "link"
            link.symlink_to(file)
            with self.assertRaises(OSError): token_file(str(link))
            file.chmod(0o644)
            with self.assertRaises(FixtureError): token_file(str(file))
            file.chmod(0o600); file.write_text("x" * 130)
            with self.assertRaises(FixtureError): token_file(str(file))
            with self.assertRaises(FixtureError): token_file("relative-token")

    def test_http_handler_rejects_duplicate_headers_chunking_and_large_bodies(self):
        for pairs in [[("Content-Length", "1"), ("Content-Length", "1")],
                      [("Content-Length", "0"), ("Transfer-Encoding", "chunked")],
                      [("Content-Length", str(MAX_BODY + 1))], []]:
            with self.subTest(headers=pairs):
                handler = object.__new__(Handler)
                handler.headers = Message()
                for key, value in pairs: handler.headers[key] = value
                handler.wfile = io.BytesIO()
                status = []
                handler.send_response = status.append
                handler.send_header = lambda *_: None
                handler.end_headers = lambda: None
                # No provider or body reader: invalid admission must fail first.
                handler.do_POST()
                self.assertIn(status[0], [400, 413])
                self.assertEqual(handler.wfile.getvalue(), b'{"error":"fixture_request_rejected"}')


if __name__ == "__main__":
    unittest.main()
