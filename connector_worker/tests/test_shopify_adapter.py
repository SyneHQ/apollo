"""Deterministic Shopify shapes, with real dlt iteration and durable handoff."""
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch

from syne_connectors.handoff import write_pages
from syne_connectors.manifest import ConnectorError, validate_manifest
from syne_connectors.shopify.adapter import shopify_pages
from syne_connectors.shopify.queries import QUERIES
from syne_connectors.shopify.records import normalize_shopify
from syne_connectors.shopify import windows
from syne_connectors.transport import BoundedSession, Budget
from test_pages import Sink, budget
from test_shopify_transport import response

NOW = datetime(2026, 9, 22, 12, tzinfo=timezone.utc)
VALUES = {"store": "merchant", "access_token": "secret-canary", "start_date": "2026-09-01", "sync_mode": "incremental"}
MANIFEST = validate_manifest((Path(__file__).parent / "fixtures/shopify.json").read_bytes())


def bag(amount="9999999999999.123456", currency="USD"):
    return {"shopMoney": {"amount": amount, "currencyCode": currency},
            "presentmentMoney": {"amount": "987.65", "currencyCode": "EUR"}}


def order(number=1, date="2026-09-10T00:00:00Z"):
    row = {"id": f"gid://shopify/Order/{number}", "createdAt": "2026-09-01T00:00:00Z",
           "updatedAt": date, "processedAt": date, "cancelledAt": None,
           "displayFinancialStatus": "PARTIALLY_REFUNDED", "currencyCode": "USD", "presentmentCurrencyCode": "EUR"}
    for name in ["totalPriceSet", "currentTotalPriceSet", "currentSubtotalPriceSet", "currentTotalTaxSet",
                 "currentTotalDiscountsSet", "totalReceivedSet", "totalRefundedSet", "netPaymentSet"]:
        row[name] = bag()
    return row


def refund(number):
    return {"id": f"gid://shopify/Refund/{number}", "createdAt": "2026-09-09T00:00:00Z",
            "updatedAt": "2026-09-10T00:00:00Z", "totalRefundedSet": bag("12.30")}


def line(number):
    return {"id": f"gid://shopify/LineItem/{number}", "sku": None, "title": "Original product title",
            "quantity": 3, "currentQuantity": 2, "originalUnitPriceSet": bag(), "discountedTotalSet": bag("123.45")}


def transaction(number):
    return {"id": f"gid://shopify/OrderTransaction/{number}", "createdAt": "2026-09-09T00:00:00Z",
            "processedAt": None, "kind": "REFUND", "status": "FAILURE", "gateway": "test", "amountSet": bag("12.30")}


class Merchant:
    def __init__(self):
        self.orders = [order()]
        self.refunds = {self.orders[0]["id"]: [refund(1), refund(2)]}
        self.lines = {self.orders[0]["id"]: [line(i) for i in range(1, 56)]}
        self.transactions = {refund(1)["id"]: [transaction(i) for i in range(1, 28)],
                             refund(2)["id"]: [transaction(99)]}
        self.requests = []; self.page_limit = 100; self.scopes = ["read_orders"]

    def connection(self, records, values):
        offset = int(values["after"] or 0)
        end = min(offset + values["first"], offset + self.page_limit, len(records))
        return {"nodes": deepcopy(records[offset:end]), "pageInfo": {"hasNextPage": end < len(records), "endCursor": str(end) if end else None}}

    def send(self, request, _url):
        body = json.loads(request.body)
        name = next(key for key, query in QUERIES.items() if query == body["query"])
        values = body["variables"]; self.requests.append((name, deepcopy(values)))
        if name == "access":
            return response({"data": {"currentAppInstallation": {"accessScopes": [{"handle": s} for s in self.scopes]}}})
        if name in {"orders", "order_ids", "refunds"}:
            start, end = re.findall(r"updated_at:>='([^']+)' updated_at:<'([^']+)'", values["filter"])[0]
            rows = [deepcopy(o) for o in self.orders if windows.stamp(start) <= windows.stamp(o["updatedAt"]) < windows.stamp(end)]
            rows.sort(key=lambda o: (o["updatedAt"], o["id"]))
            if name != "orders":
                rows = [{k: o[k] for k in ["id", "createdAt", "updatedAt"]} for o in rows]
            if name == "refunds":
                rows = [{**o, "refunds": deepcopy(self.refunds.get(o["id"], []))} for o in rows]
            return response({"data": {"orders": self.connection(rows, values)}})
        if name == "line_items":
            parent = next(o for o in self.orders if o["id"] == values["id"])
            return response({"data": {"order": {"id": parent["id"], "updatedAt": parent["updatedAt"], "lineItems": self.connection(self.lines[parent["id"]], values)}}})
        for oid, refunds in self.refunds.items():
            for row in refunds:
                if row["id"] == values["id"]:
                    return response({"data": {"refund": {"id": row["id"], "updatedAt": row["updatedAt"], "order": {"id": oid},
                        "transactions": self.connection(self.transactions[row["id"]], values)}}})
        raise AssertionError("Unexpected fixture request")


def run(merchant, name, sink=None, *, values=None, now=NOW, batch_rows=250, b=None):
    sink = sink or Sink(); b = b or budget()
    stream = next(stream for stream in MANIFEST["streams"] if stream["id"] == name)
    def source(_session, request, url): return merchant.send(request, url)
    with patch.object(BoundedSession, "_once", source):
        pages = shopify_pages(MANIFEST, stream, values or VALUES, sink.state["checkpoint"], b, now=now)
        try:
            result = write_pages(pages, sink, sink.state, "test-run", lambda row: normalize_shopify(MANIFEST, stream, row), b, batch_rows=batch_rows)
        finally:
            pages.close()
    return sink, result


class ShopifyAdapterTests(unittest.TestCase):
    def test_all_financial_streams_preserve_exact_money_and_failed_refunds(self):
        for name, count in [("orders", 1), ("line_items", 55), ("refunds", 2), ("refund_transactions", 28)]:
            with self.subTest(stream=name):
                sink, result = run(Merchant(), name)
                self.assertEqual(len(sink.rows), count); self.assertTrue(sink.state["checkpoint"]["done"])
                self.assertEqual(result["records_committed"], count)
                row = next(iter(sink.rows.values())); fields = row["payload"]["fields"]
                self.assertEqual(fields["currency"], "USD"); self.assertFalse(row["deleted"])
                if name == "orders":
                    self.assertEqual(fields["original_total"], "9999999999999.123456")
                    self.assertEqual(row["payload"]["source"]["record"]["totalPriceSet"]["presentmentMoney"]["currencyCode"], "EUR")
                if name == "refunds": self.assertEqual(fields["recorded_amount"], "12.30")
                if name == "refund_transactions":
                    self.assertEqual(fields["status"], "FAILURE"); self.assertEqual(fields["kind"], "REFUND")
                    self.assertEqual(fields["amount"], "12.30"); self.assertIsNone(fields["processed_at"])

    def test_child_cursor_resume_does_not_repeat_earlier_pages(self):
        for name in ["line_items", "refund_transactions"]:
            merchant = Merchant(); sink = Sink(); commit = sink.commit
            def crash(raw):
                if sink.state["sequence"] == 1: raise ConnectorError("worker_stopped")
                return commit(raw)
            sink.commit = crash
            with self.assertRaisesRegex(ConnectorError, "worker_stopped"): run(merchant, name, sink)
            self.assertEqual(sink.state["checkpoint"]["position"]["child"]["after"], "25")
            merchant.requests.clear(); sink.commit = commit
            sink, result = run(merchant, name, sink)
            child_requests = [v for n, v in merchant.requests if n == name]
            self.assertEqual(child_requests[0]["after"], "25")
            self.assertEqual(len(sink.rows), 55 if name == "line_items" else 28)
            self.assertTrue(sink.state["checkpoint"]["done"])

    def test_resume_rejects_changed_parent_before_skipping_child_records(self):
        merchant = Merchant(); sink = Sink(); commit = sink.commit
        def crash(raw):
            if sink.state["sequence"] == 1: raise ConnectorError("worker_stopped")
            return commit(raw)
        sink.commit = crash
        with self.assertRaises(ConnectorError): run(merchant, "line_items", sink)
        merchant.orders[0]["updatedAt"] = "2026-09-11T00:00:00Z"
        sink.commit = commit; before = len(sink.calls)
        with self.assertRaisesRegex(ConnectorError, "shopify_parent_changed"): run(merchant, "line_items", sink)
        self.assertEqual(len(sink.calls), before)

    def test_partial_child_page_resume_checks_content_hash(self):
        merchant = Merchant(); merchant.lines[merchant.orders[0]["id"]] = [line(1), line(2), line(3)]
        sink = Sink(); commit = sink.commit
        def crash(raw):
            if sink.state["sequence"] == 1: raise ConnectorError("worker_stopped")
            return commit(raw)
        sink.commit = crash
        with self.assertRaises(ConnectorError): run(merchant, "line_items", sink, batch_rows=1)
        self.assertEqual(sink.state["checkpoint"]["row_offset"], 1)
        sink.commit = commit
        merchant.lines[merchant.orders[0]["id"]][0]["title"] = "Changed"
        with self.assertRaisesRegex(ConnectorError, "source_page_changed"): run(merchant, "line_items", sink, batch_rows=1)
        merchant.lines[merchant.orders[0]["id"]][0]["title"] = "Original product title"
        sink, result = run(merchant, "line_items", sink, batch_rows=1)
        self.assertEqual(result["records_committed"], 2); self.assertEqual(len(sink.rows), 3)

    def test_dense_window_replay_finishes_without_duplicate_current_rows(self):
        merchant = Merchant(); merchant.page_limit = 1
        merchant.orders = [order(i, f"2026-09-{i + 1:02d}T00:00:00Z") for i in range(1, 7)]
        with patch.object(windows, "SPLIT_AT", 2): sink, result = run(merchant, "orders")
        self.assertTrue(sink.state["checkpoint"]["done"]); self.assertEqual(len(sink.rows), 6)
        self.assertGreater(result["records_committed"], 6)
        self.assertGreater(len({v["filter"] for name, v in merchant.requests if name == "orders"}), 1)

    def test_updates_and_full_refresh_preserve_identity_without_assuming_deletion(self):
        merchant = Merchant(); sink, _ = run(merchant, "orders")
        merchant.orders[0]["updatedAt"] = "2026-09-22T12:00:01Z"
        merchant.orders[0]["currentTotalPriceSet"] = bag("1.20")
        sink, _ = run(merchant, "orders", sink, now=NOW + timedelta(seconds=2))
        self.assertEqual(len(sink.rows), 1)
        self.assertEqual(next(iter(sink.rows.values()))["payload"]["fields"]["current_total"], "1.20")
        merchant.orders.clear()
        sink, _ = run(merchant, "orders", sink, now=NOW + timedelta(seconds=3))
        self.assertEqual(len(sink.rows), 1)
        merchant = Merchant(); values = {**VALUES, "sync_mode": "full_refresh"}
        sink, _ = run(merchant, "orders", values=values)
        merchant.requests.clear(); run(merchant, "orders", sink, values=values, now=NOW + timedelta(seconds=1))
        self.assertIn("updated_at:>='2026-09-01T00:00:00Z'", merchant.requests[1][1]["filter"])

    def test_lost_ack_retries_identical_child_batch_and_cancellation_resumes(self):
        merchant = Merchant(); sink = Sink(); commit = sink.commit; first = True
        def lost(raw):
            nonlocal first
            result = commit(raw)
            if first: first = False; raise ConnectionError()
            return result
        sink.commit = lost
        with patch.object(Budget, "wait"): run(merchant, "line_items", sink)
        self.assertEqual(sink.calls[0], sink.calls[1]); self.assertEqual(len(sink.rows), 55)
        sink = Sink(); commit = sink.commit; b = budget()
        def cancel(raw):
            result = commit(raw); b.cancelled.set(); return result
        sink.commit = cancel
        with self.assertRaisesRegex(ConnectorError, "cancelled"): run(merchant, "line_items", sink, b=b)
        self.assertFalse(sink.state["checkpoint"]["done"])
        sink.commit = commit; run(merchant, "line_items", sink)
        self.assertTrue(sink.state["checkpoint"]["done"])

    def test_bad_money_and_insufficient_history_fail_before_record_commits(self):
        merchant = Merchant(); merchant.orders[0]["totalPriceSet"]["shopMoney"]["amount"] = 1.2
        sink = Sink()
        with self.assertRaisesRegex(ConnectorError, "source_money_format_changed"): run(merchant, "orders", sink)
        self.assertEqual(sink.calls, [])
        merchant = Merchant(); merchant.orders[0]["netPaymentSet"]["shopMoney"]["currencyCode"] = "INR"
        with self.assertRaisesRegex(ConnectorError, "source_currency_mismatch"): run(merchant, "orders", sink)
        with self.assertRaisesRegex(ConnectorError, "shopify_history_scope_required"):
            run(Merchant(), "orders", sink, values={**VALUES, "start_date": "2026-01-01"})
        self.assertEqual(sink.calls, [])
