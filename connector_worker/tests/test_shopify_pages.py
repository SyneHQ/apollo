from copy import deepcopy
from datetime import datetime, timezone
import json
import unittest
from unittest.mock import patch

from syne_connectors.manifest import ConnectorError
from syne_connectors.shopify.client import ShopifyClient
from syne_connectors.shopify.transport import ShopifySession
from syne_connectors.shopify import windows
from syne_connectors.transport import BoundedSession
from test_pages import budget
from test_shopify_transport import LIMITS, response

NOW = datetime(2026, 9, 22, 12, tzinfo=timezone.utc)
VALUES = {"start_date": "2026-01-01", "sync_mode": "incremental"}


def client():
    b = budget()
    return ShopifyClient(ShopifySession("merchant.myshopify.com", LIMITS, b), "secret-canary", b)


def order_response(nodes, more=False, after=None):
    return response({"data": {"orders": {"nodes": nodes, "pageInfo": {"hasNextPage": more, "endCursor": after}}}})


class ShopifyPaginationTests(unittest.TestCase):
    def test_real_dlt_posts_and_updates_body_cursor_including_empty_nonterminal_page(self):
        seen = []
        def source(_session, request, _url):
            body = json.loads(request.body); seen.append(body["variables"]["after"])
            if len(seen) == 1:
                return order_response([], True, "cursor-1")
            return order_response([{"id": "gid://shopify/Order/2"}], False, "cursor-2")
        with patch.object(BoundedSession, "_once", source):
            pages = list(client().pages("order_ids", {"first": 25, "after": None, "filter": "created_at:>=2026-01-01"}, ["data", "orders"]))
        self.assertEqual(seen, [None, "cursor-1"])
        self.assertTrue(pages[0].more); self.assertFalse(pages[1].more)
        self.assertEqual(pages[1].records[0]["id"], "gid://shopify/Order/2")

    def test_malformed_and_repeated_cursors_fail_without_source_body_leak(self):
        for info in [{}, {"hasNextPage": "true", "endCursor": "x"},
                     {"hasNextPage": True, "endCursor": None},
                     {"hasNextPage": True, "endCursor": "resume"}]:
            data = {"data": {"orders": {"nodes": [{"secret": "secret-canary"}], "pageInfo": info}}}
            with patch.object(BoundedSession, "_once", return_value=response(data)):
                with self.assertRaises(ConnectorError) as err:
                    list(client().pages("order_ids", {"first": 25, "after": "resume", "filter": "x"}, ["data", "orders"]))
                self.assertNotIn("secret-canary", str(err.exception))
        with patch.object(BoundedSession, "_once", return_value=order_response([], True, "same")):
            with self.assertRaisesRegex(ConnectorError, "source_cursor_loop"):
                list(client().pages("order_ids", {"first": 25, "after": None, "filter": "x"}, ["data", "orders"]))

    def test_connection_cap_includes_previously_committed_pages(self):
        with patch.object(BoundedSession, "_once", return_value=order_response([{"id": "one"}], False, "final")):
            with self.assertRaisesRegex(ConnectorError, "shopify_pagination_limit"):
                list(client().pages("order_ids", {"first": 25, "after": "resume", "filter": "x"}, ["data", "orders"], count=25000))

    def test_scopes_are_checked_for_requested_history(self):
        for scopes, start, error in [([], "2026-09-01", "shopify_read_orders_required"),
                (["read_orders"], "2026-01-01", "shopify_history_scope_required"),
                (["read_orders", "read_all_orders"], "2026-09-23", "shopify_history_in_future"),
                (["read_orders"], "2026-09-01", None),
                (["read_orders", "read_all_orders"], "2026-01-01", None)]:
            data = {"data": {"currentAppInstallation": {"accessScopes": [{"handle": s} for s in scopes]}}}
            with self.subTest(scopes=scopes, start=start), patch.object(BoundedSession, "_once", return_value=response(data)):
                if error:
                    with self.assertRaisesRegex(ConnectorError, error): client().check_access(start, NOW)
                else:
                    client().check_access(start, NOW)

    def test_missing_or_null_parent_is_not_an_empty_success(self):
        for data in [{"data": {"order": None}}, {"data": {"order": {"id": "one"}}}]:
            with patch.object(BoundedSession, "_once", return_value=response(data)):
                with self.assertRaisesRegex(ConnectorError, "source_schema_changed"):
                    list(client().pages("line_items", {"id": "gid://shopify/Order/1", "first": 25, "after": None}, ["data", "order", "lineItems"]))


class ShopifyWindowTests(unittest.TestCase):
    def test_dense_windows_replay_then_cover_both_halves_before_watermark(self):
        start = windows.initial(VALUES, {}, NOW); p = start["position"]
        left = windows.next_parent(p, "limit", True, windows.SPLIT_AT)
        self.assertFalse(left["done"]); self.assertIsNone(left["position"]["after"])
        middle = left["position"]["end"]
        self.assertEqual(left["position"]["pending"], [[middle, p["end"]]])
        self.assertEqual(windows.initial(VALUES, left, NOW), left)
        right = windows.next_parent(left["position"], None, False, 10)
        self.assertEqual(right["position"]["start"], middle); self.assertFalse(right["done"])
        complete = windows.next_parent(right["position"], None, False, 20)
        self.assertTrue(complete["done"])
        renewed = windows.initial(VALUES, complete, NOW)
        self.assertEqual(renewed["position"]["start"], p["end"] - windows.OVERLAP)
        full_values = {**VALUES, "sync_mode": "full_refresh"}
        full = windows.initial(full_values, {}, NOW)
        complete = windows.next_parent(full["position"], None, False, 0)
        self.assertEqual(windows.initial(full_values, complete, NOW)["position"]["start"], full["position"]["start"])

    def test_single_second_density_and_tampered_checkpoint_fail(self):
        start = windows.initial(VALUES, {}, NOW)
        p = deepcopy(start["position"]); p["start"] = p["end"] - 1
        with self.assertRaisesRegex(ConnectorError, "shopify_dense_window"):
            windows.next_parent(p, "more", True, windows.SPLIT_AT)
        for mutation in [{"scan_end": int(NOW.timestamp()) + 10}, {"pending": [[1, 2]]},
                         {"count": -1}, {"history": "2020-01-01"}, {"v": 2}, {"after": "x" * 4097}]:
            changed = deepcopy(start); changed["position"].update(mutation)
            with self.assertRaisesRegex(ConnectorError, "checkpoint_invalid"):
                windows.initial(VALUES, changed, NOW)

    def test_source_window_filter_is_verified_and_half_open(self):
        p = windows.initial(VALUES, {}, NOW)["position"]
        row = {"id": "gid://shopify/Order/1", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-02-01T00:00:00Z"}
        windows.check_order(row, p)
        self.assertIn("updated_at:<'2026-09-22T12:00:00Z'", windows.search(p))
        for changed in [{**row, "updatedAt": "2026-09-22T12:00:00Z"},
                        {**row, "createdAt": "2025-12-31T23:59:59Z"},
                        {**row, "updatedAt": "2026-02-01T00:00:00"}]:
            with self.assertRaises(ConnectorError): windows.check_order(changed, p)
