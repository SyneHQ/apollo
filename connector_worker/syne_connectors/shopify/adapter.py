"""Shopify extraction with durable parent/child coordinates and bounded pages."""
from contextlib import closing
from copy import deepcopy
from datetime import datetime, timezone
from hashlib import sha256

from ..manifest import configuration, require
from ..pages import Page, source_bytes
from .client import ShopifyClient, at
from .records import gid
from .transport import ShopifySession
from . import windows

CHILD_KEYS = {"parent_hash", "refund_index", "after", "count"}


def child_state(checkpoint, parent_hash, refund_index, after, count):
    p = deepcopy(checkpoint["position"])
    p["child"] = {"parent_hash": parent_hash, "refund_index": refund_index, "after": after, "count": count}
    return {"position": p, "done": False}


def check_child(child, parent_hash, refunds, line_items):
    require(isinstance(child, dict) and set(child) == CHILD_KEYS and windows.cursor(child["after"])
            and type(child["count"]) is int and 0 <= child["count"] < 25000, "checkpoint_invalid")
    require(child["parent_hash"] == parent_hash, "shopify_parent_changed")
    index = child["refund_index"]
    require(index is None if line_items else type(index) is int and 0 <= index < len(refunds), "checkpoint_invalid")


def nested_pages(client, name, order, current, parent_after):
    """Refetch one parent on resume; persist each child cursor independently."""
    parent_hash = sha256(source_bytes(order)).hexdigest()
    line_items = name == "line_items"
    refunds = [] if line_items else at(order, ["refunds"])
    require(isinstance(refunds, list), "source_schema_changed")
    ids = [gid(refund.get("id"), "Refund") for refund in refunds if isinstance(refund, dict)]
    require(len(ids) == len(refunds) == len(set(ids)), "source_identity_invalid")
    for refund in refunds:
        windows.stamp(refund.get("createdAt"))
        windows.stamp(refund.get("updatedAt"))
    child = current["position"]["child"]
    if child is not None:
        check_child(child, parent_hash, refunds, line_items)
    indices = [None] if line_items else range(child["refund_index"] if child else 0, len(refunds))
    if not line_items and not refunds:
        require(child is None, "shopify_parent_changed")
        yield Page([], current, parent_after)
        return
    for index in indices:
        refund = None if line_items else refunds[index]
        entity = order if line_items else refund
        entity_key, connection = ("order", "lineItems") if line_items else ("refund", "transactions")
        after, count = (child["after"], child["count"]) if child else (None, 0)
        variables = {"id": entity["id"], "first": 25, "after": after}
        with closing(client.pages(name, variables, ["data", entity_key, connection], count=count)) as pages:
            for page in pages:
                parent = at(page.response, ["data", entity_key])
                require(parent.get("id") == entity["id"] and parent.get("updatedAt") == entity.get("updatedAt"), "shopify_parent_changed")
                if not line_items:
                    require(at(parent, ["order", "id"]) == order["id"], "source_identity_invalid")
                count += len(page.records)
                require(not page.more or count < 25000, "shopify_child_pagination_limit")
                records = [{"record": row, "order": {key: order[key] for key in ["id", "createdAt", "updatedAt"]},
                            **({"refund": {key: refund[key] for key in ["id", "createdAt", "updatedAt"]}} if refund else {})}
                           for row in page.records]
                if page.more:
                    following = child_state(current, parent_hash, index, page.after, count)
                elif not line_items and index + 1 < len(refunds):
                    following = child_state(current, parent_hash, index + 1, None, 0)
                else:
                    following = parent_after
                yield Page(records, current, following)
                current = following
        child = None


def shopify_pages(manifest, stream, values, checkpoint, budget, *, now=None, session_factory=ShopifySession):
    require(manifest["id"] == "syne/shopify" and manifest["version"] == "1.1.0"
            and manifest["runtime"]["kind"] == "reviewed_adapter"
            and manifest["runtime"]["adapter"] == "shopify_graphql_v1", "connector_upgrade_required")
    values = configuration(manifest, values)
    name = stream["id"]
    require(name in {"orders", "line_items", "refunds", "refund_transactions"}, "worker_stream_invalid")
    now = now or datetime.now(timezone.utc)
    current = windows.initial(values, checkpoint, now)
    host = values["store"] + ".myshopify.com"
    with session_factory(host, manifest["limits"], budget) as session:
        client = ShopifyClient(session, values["access_token"], budget)
        client.check_access(values["start_date"], now)
        while not current["done"]:
            p = current["position"]
            operation = {"orders": "orders", "line_items": "order_ids", "refunds": "refunds", "refund_transactions": "refunds"}[name]
            variables = {"first": 25 if name == "orders" else 1, "after": p["after"], "filter": windows.search(p)}
            with closing(client.pages(operation, variables, ["data", "orders"], count=p["count"])) as parents:
                for page in parents:
                    for order in page.records:
                        windows.check_order(order, p)
                    count = current["position"]["count"] + len(page.records)
                    following = windows.next_parent(current["position"], page.after, page.more, count)
                    if current["position"]["child"] is not None:
                        require(name in {"line_items", "refund_transactions"} and len(page.records) == 1, "shopify_parent_changed")
                    if name == "orders" or not page.records:
                        yield Page([{"record": row, "order": {key: row[key] for key in ["id", "createdAt", "updatedAt"]}} for row in page.records], current, following)
                    elif name == "refunds":
                        order = page.records[0]
                        refunds = at(order, ["refunds"])
                        require(isinstance(refunds, list) and all(isinstance(row, dict) for row in refunds), "source_schema_changed")
                        yield Page([{"record": row, "order": {key: order[key] for key in ["id", "createdAt", "updatedAt"]}} for row in refunds], current, following)
                    else:
                        yield from nested_pages(client, name, page.records[0], current, following)
                    current = following
                    if current["done"] or any(current["position"][key] != p[key] for key in ["start", "end"]):
                        break
