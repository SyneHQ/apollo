"""Validate declared fields while retaining source values separately."""
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation
from hashlib import sha256
import re

from .manifest import require
from .pages import source_bytes


def normalize_record(manifest, stream, row):
    require(isinstance(row, dict), "source_schema_changed")
    fields = {}
    for column in stream["columns"]:
        key, kind = column["key"], column["type"]
        if key == "payload" and kind == "json":
            continue
        value = row.get(key)
        if value is None:
            require(column["nullable"], "source_required_field_missing")
            fields[key] = None
            continue
        if kind == "string":
            require(isinstance(value, str), "source_field_type_changed")
        elif kind in {"integer_string", "decimal_string"}:
            require(type(value) in {str, int, Decimal}, "source_field_type_changed")
            value = str(value)
            pattern = r"-?(0|[1-9][0-9]*)" if kind == "integer_string" else r"-?(0|[1-9][0-9]*)(\.[0-9]+)?"
            require(len(value) <= 128 and re.fullmatch(pattern, value), "source_money_format_changed")
        elif kind == "boolean":
            require(type(value) is bool, "source_field_type_changed")
        elif kind == "timestamp":
            # Razorpay documents these timestamps as Unix seconds. Other REST
            # sources must supply an offset-bearing ISO timestamp or an adapter.
            if manifest["id"] == "syne/razorpay" and type(value) is int:
                try:
                    value = datetime.fromtimestamp(value, timezone.utc).isoformat().replace("+00:00", "Z")
                except (ValueError, OSError, OverflowError):
                    require(False, "source_timestamp_invalid")
            require(isinstance(value, str), "source_timestamp_invalid")
            try:
                parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
                require(parsed.tzinfo is not None, "source_timestamp_timezone_missing")
            except ValueError:
                require(False, "source_timestamp_invalid")
        fields[key] = value
    keys = [fields[key] for key in stream["primaryKey"]]
    require(all(value is not None and type(value) in {str, bool} for value in keys), "source_primary_key_invalid")
    record_id = keys[0] if len(keys) == 1 and isinstance(keys[0], str) else source_bytes(keys).decode("utf-8")
    require(bool(record_id), "source_primary_key_invalid")
    if len(record_id.encode("utf-8")) > 512:
        record_id = "sha256:" + sha256(source_bytes(keys)).hexdigest()
    deleted = False
    if stream["sync"].get("deletionPolicy") == "explicit_tombstone":
        deleted = fields[stream["sync"]["tombstoneField"]]
        require(type(deleted) is bool, "source_tombstone_invalid")
    return {"id":record_id, "payload":{"source":row, "fields":fields}, "deleted":deleted}
