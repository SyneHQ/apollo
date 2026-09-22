"""Durable, half-open Shopify update windows; no pagination-cap truncation."""
from copy import deepcopy
from datetime import datetime, timezone
import re

from ..manifest import require

VERSION = 1
OVERLAP = 7 * 24 * 60 * 60
SPLIT_AT = 20000
POSITION_KEYS = {"v", "history", "mode", "scan_end", "start", "end", "pending", "after", "count", "child"}


def utc(epoch):
    return datetime.fromtimestamp(epoch, timezone.utc).isoformat().replace("+00:00", "Z")


def stamp(value):
    require(isinstance(value, str) and len(value) <= 64, "source_timestamp_invalid")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        require(parsed.tzinfo is not None, "source_timestamp_timezone_missing")
        return parsed.timestamp()
    except (ValueError, OverflowError):
        require(False, "source_timestamp_invalid")


def cursor(value):
    return value is None or (isinstance(value, str) and 0 < len(value) <= 4096)


def initial(values, checkpoint, now):
    history, mode = values["start_date"], values["sync_mode"]
    lower = int(datetime.fromisoformat(history).replace(tzinfo=timezone.utc).timestamp())
    end = int(now.timestamp())
    require(lower <= end, "shopify_history_in_future")
    if checkpoint:
        require(isinstance(checkpoint, dict) and set(checkpoint) <= {"position", "done", "page_hash", "row_offset"}
                and type(checkpoint.get("done")) is bool, "checkpoint_invalid")
        p = checkpoint.get("position")
        require(isinstance(p, dict) and set(p) == POSITION_KEYS and p["v"] == VERSION
                and p["history"] == history and p["mode"] == mode, "checkpoint_invalid")
        require(all(type(p[k]) is int for k in ["scan_end", "start", "end", "count"]), "checkpoint_invalid")
        require(lower <= p["start"] <= p["end"] <= p["scan_end"] <= end
                and 0 <= p["count"] <= SPLIT_AT + 100 and cursor(p["after"]), "checkpoint_invalid")
        require(isinstance(p["pending"], list) and len(p["pending"]) <= 64, "checkpoint_invalid")
        expected = p["end"]
        for window in p["pending"]:
            require(isinstance(window, list) and len(window) == 2 and all(type(t) is int for t in window)
                    and window[0] == expected and window[0] < window[1] <= p["scan_end"], "checkpoint_invalid")
            expected = window[1]
        require(expected == p["scan_end"], "checkpoint_invalid")
        require(p["child"] is None or isinstance(p["child"], dict), "checkpoint_invalid")
        if not checkpoint["done"]:
            return deepcopy(checkpoint)
        require(p["child"] is None and not p["pending"] and p["end"] == p["scan_end"], "checkpoint_invalid")
        if mode == "incremental":
            lower = max(lower, p["scan_end"] - OVERLAP)
    return {"position": {"v": VERSION, "history": history, "mode": mode, "scan_end": end,
                         "start": lower, "end": end, "pending": [], "after": None, "count": 0, "child": None},
            "done": False}


def search(position):
    # Every interpolation has passed a date/integer check, never arbitrary syntax.
    return (f"created_at:>='{position['history']}T00:00:00Z' "
            f"updated_at:>='{utc(position['start'])}' updated_at:<'{utc(position['end'])}'")


def check_order(order, position):
    require(isinstance(order, dict) and isinstance(order.get("id"), str)
            and re.fullmatch(r"gid://shopify/Order/[0-9]+", order["id"]), "source_schema_changed")
    created, updated = stamp(order.get("createdAt")), stamp(order.get("updatedAt"))
    lower = stamp(position["history"] + "T00:00:00Z")
    require(created >= lower and created <= updated and position["start"] <= updated < position["end"],
            "shopify_window_changed")


def next_parent(position, after, more, count):
    p = deepcopy(position)
    p.update(after=after, count=count, child=None)
    if more and count < SPLIT_AT:
        return {"position": p, "done": False}
    if more:
        require(p["end"] - p["start"] > 1, "shopify_dense_window")
        mid = p["start"] + (p["end"] - p["start"]) // 2
        p["pending"].insert(0, [mid, p["end"]])
        p.update(end=mid, after=None, count=0)
        require(len(p["pending"]) <= 64, "shopify_dense_window")
        return {"position": p, "done": False}
    if p["pending"]:
        p["start"], p["end"] = p["pending"].pop(0)
        p.update(after=None, count=0)
        return {"position": p, "done": False}
    return {"position": p, "done": True}
