"""Advance source progress only after a matching durable Go receipt."""
from datetime import datetime, timezone
from hashlib import sha256
from uuid import uuid4

from .manifest import ConnectorError, require
from .pages import source_bytes

MAX_BATCH_BYTES = 1 << 20
MAX_RECORD_BYTES = 128 << 10


def wire_batch(batch):
    # Matches Go encoding/json's struct order and default HTML escaping. Nested
    # objects retain their emitted order; decimals never pass through float.
    text = source_bytes(batch).decode("utf-8")
    for char, escaped in [("<",r"\u003c"),(">",r"\u003e"),("&",r"\u0026"),("\u2028",r"\u2028"),("\u2029",r"\u2029")]:
        text = text.replace(char, escaped)
    return text.encode("utf-8")


def batch_for(run_id, sequence, records, checkpoint):
    return {"id":str(uuid4()), "run_id":run_id, "expected_sequence":sequence,
            "observed_at":datetime.now(timezone.utc).isoformat().replace("+00:00","Z"),
            "records":records, "checkpoint":checkpoint}


def write_pages(pages, sink, state, run_id, normalize, budget, batch_rows=250):
    """sink.commit accepts exact request bytes and returns a decoded Receipt.

    A transport ambiguity retries the same bytes at most three times. A conflict,
    cancellation or unknown failure never advances the caller's checkpoint.
    """
    sequence = state["sequence"]
    require(type(sequence) is int and 0 <= sequence < 2**63-1, "checkpoint_invalid")
    require(1 <= batch_rows <= 1000, "batch_limit")
    committed = 0
    for page in pages:
        budget.remaining()
        start = page.start
        offset = start.get("row_offset", 0)
        require(type(offset) is int and 0 <= offset <= len(page.records), "checkpoint_invalid")
        if offset:
            require(start.get("page_hash") == page.digest, "source_page_changed")
        # Normalize a bounded source page before any partial commit, so duplicate
        # primary keys in that page cannot silently overwrite one another.
        records = [normalize(record) for record in page.records]
        require(len({r["id"] for r in records}) == len(records), "duplicate_source_id")
        for record in records:
            require(len(source_bytes(record["payload"])) <= MAX_RECORD_BYTES, "record_limit")
        while offset < len(records) or (not records and offset == 0):
            end = min(offset + batch_rows, len(records))
            while True:
                checkpoint = page.after if end == len(records) else {"position":start["position"], "done":False, "page_hash":page.digest, "row_offset":end}
                batch = batch_for(run_id, sequence, records[offset:end], checkpoint)
                encoded = wire_batch(batch)
                if len(encoded) <= MAX_BATCH_BYTES:
                    break
                require(end - offset > 1, "record_limit")
                end = offset + (end - offset) // 2
            for attempt in range(3):
                budget.remaining()
                try:
                    receipt = sink.commit(encoded)
                    break
                except (ConnectionError, TimeoutError):
                    require(attempt < 2, "destination_ack_unknown")
                    budget.wait(2**attempt)
            require(receipt.get("batch_id") == batch["id"] and receipt.get("sequence") == sequence+1
                    and receipt.get("records") == end-offset and receipt.get("digest") == sha256(encoded).hexdigest(), "destination_receipt_mismatch")
            sequence += 1
            committed += end-offset
            offset = end
            if not records:
                break
    return {"sequence":sequence, "records_committed":committed}
