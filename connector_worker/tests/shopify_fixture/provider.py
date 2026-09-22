"""Four refund cases in fixed Shopify shapes, with an explicit correction stage."""
from copy import deepcopy
from datetime import datetime
import hmac
import json
import re
import threading

from syne_connectors.shopify.queries import API_VERSION, QUERIES

SOURCE_HOST = "merchant.myshopify.com"
SOURCE_PATH = "/_fixture/shopify/graphql"
CONTROL_PATH = "/_fixture/shopify/mode"
MAX_BODY = 32768
MAX_RESPONSE = 65536
DATE = "2026-09-10T00:00:00Z"


class FixtureError(Exception):
    def __init__(self, status=400):
        self.status = status
        super().__init__("Fixture request rejected")


def checked(condition):
    if not condition:
        raise FixtureError()


def decode(raw):
    checked(isinstance(raw, bytes) and len(raw) <= MAX_BODY)
    def pairs(items):
        result = {}
        for key, value in items:
            checked(key not in result)
            result[key] = value
        return result
    try:
        return json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(FixtureError()))
    except (ValueError, UnicodeError, RecursionError):
        raise FixtureError() from None


def money(amount, currency="USD"):
    return {"shopMoney": {"amount": amount, "currencyCode": currency},
            "presentmentMoney": {"amount": amount, "currencyCode": currency}}


def dataset(mode):
    checked(mode in {"baseline", "corrected"})
    order = {"id": "gid://shopify/Order/1", "createdAt": "2026-09-01T00:00:00Z", "updatedAt": DATE}
    refunds, transactions = [], {}
    for index, amount in enumerate(["12.30", "7.00", "4.00" if mode == "corrected" else "5.00", "2.00"], 1):
        refund = {"id": f"gid://shopify/Refund/{index}", "createdAt": "2026-09-09T00:00:00Z",
                  "updatedAt": DATE, "totalRefundedSet": money(amount)}
        refunds.append(refund)
        transactions[refund["id"]] = [{"id": f"gid://shopify/OrderTransaction/{index}",
            "createdAt": "2026-09-09T00:00:00Z", "processedAt": None, "kind": "REFUND",
            "status": "FAILURE" if index == 2 and mode == "baseline" else "SUCCESS", "gateway": "fixture",
            "amountSet": money(["12.30", "7.00", "4.00", "2.00"][index - 1], "EUR" if index == 4 else "USD")}]
    return order, refunds, transactions


def connection(records, values):
    after = values["after"]
    checked(after is None or (isinstance(after, str) and re.fullmatch(r"0|[1-9][0-9]{0,3}", after)))
    offset = int(after or 0)
    checked(offset <= len(records))
    end = min(offset + values["first"], len(records))
    return {"nodes": deepcopy(records[offset:end]), "pageInfo": {"hasNextPage": end < len(records), "endCursor": str(end) if end else None}}


def query_response(raw, mode):
    body = decode(raw)
    checked(isinstance(body, dict) and set(body) == {"query", "variables"})
    operation = next((name for name, query in QUERIES.items() if query == body["query"]), None)
    # Only operations used by the two financial streams are supported.
    checked(operation in {"access", "refunds", "refund_transactions"})
    values = body["variables"]
    keys = set() if operation == "access" else {"first", "after", "filter" if operation == "refunds" else "id"}
    checked(isinstance(values, dict) and set(values) == keys)
    if operation == "access":
        return {"data": {"currentAppInstallation": {"accessScopes": [{"handle": name} for name in ["read_orders", "read_all_orders"]]}}}
    checked(type(values["first"]) is int and 1 <= values["first"] <= 100)
    order, refunds, transactions = dataset(mode)
    if operation == "refunds":
        text = values["filter"]
        checked(isinstance(text, str) and len(text) <= 1024)
        match = re.fullmatch(r"created_at:>='([^']+)' updated_at:>='([^']+)' updated_at:<'([^']+)'", text)
        checked(match is not None)
        try:
            history, start, until = (datetime.fromisoformat(value.replace("Z", "+00:00")) for value in match.groups())
            checked(all(value.tzinfo is not None for value in [history, start, until]) and start < until)
            selected = (start <= datetime.fromisoformat(DATE.replace("Z", "+00:00")) < until
                        and history <= datetime.fromisoformat(order["createdAt"].replace("Z", "+00:00")))
        except (ValueError, TypeError):
            raise FixtureError() from None
        rows = [{**order, "refunds": refunds}] if selected else []
        return {"data": {"orders": connection(rows, values)}}
    checked(isinstance(values["id"], str) and values["id"] in transactions)
    refund = next(row for row in refunds if row["id"] == values["id"])
    return {"data": {"refund": {"id": refund["id"], "updatedAt": refund["updatedAt"], "order": {"id": order["id"]},
                              "transactions": connection(transactions[refund["id"]], values)}}}


class FixtureProvider:
    """Process-local stage; mode changes use a distinct operator capability."""
    def __init__(self, source_token, control_token):
        checked(all(isinstance(token, str) and re.fullmatch(r"[A-Za-z0-9_-]{32,128}", token) for token in [source_token, control_token]))
        checked(source_token != control_token)
        self.source_token, self.control_token = source_token, control_token
        self.mode = "baseline"
        self.requests = 0
        self.lock = threading.Lock()

    def respond(self, method, path, headers, raw):
        if method != "POST" or path not in {SOURCE_PATH, CONTROL_PATH}:
            raise FixtureError(404)
        checked(headers.get("content-type", "").lower() == "application/json")
        expected = self.source_token if path == SOURCE_PATH else self.control_token
        supplied = headers.get("x-shopify-access-token" if path == SOURCE_PATH else "x-fixture-control", "")
        if not isinstance(supplied, str) or not hmac.compare_digest(supplied.encode(), expected.encode()):
            raise FixtureError(403)
        with self.lock:
            if path == CONTROL_PATH:
                body = decode(raw)
                checked(isinstance(body, dict) and set(body) == {"mode"} and isinstance(body["mode"], str)
                        and body["mode"] in {"baseline", "corrected"})
                self.mode = body["mode"]
                result = {"mode": self.mode}
            else:
                if self.requests >= 10000:
                    raise FixtureError(429)
                result = query_response(raw, self.mode)
                self.requests += 1
        data = json.dumps(result, separators=(",", ":")).encode()
        checked(len(data) <= MAX_RESPONSE)
        return 200, {"Content-Type": "application/json", "Content-Length": str(len(data)), "X-Shopify-API-Version": API_VERSION}, data
