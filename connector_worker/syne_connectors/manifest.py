"""Portable v1 validation. Manifests are data, never execution grants."""
from datetime import date
from hashlib import sha256
from importlib.resources import files
import json
import re
from urllib.parse import urlsplit
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from jsonschema import Draft202012Validator


class ConnectorError(Exception):
    """Public errors contain codes only; provider payloads may contain secrets."""


def require(condition, code="invalid_manifest"):
    if not condition:
        raise ConnectorError(code)


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate_json_key")
        result[key] = value
    return result


def parse_json(raw: bytes, limit: int):
    require(len(raw) <= limit, "payload_limit")
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_pairs,
                          parse_constant=lambda _: require(False, "invalid_json"))
    except (ValueError, UnicodeError, RecursionError):
        raise ConnectorError("invalid_json") from None


def canonical(value) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":"), allow_nan=False).encode("utf-8")


def content_digest(value) -> str:
    return sha256(canonical(value)).hexdigest()


def _accepts(field, value):
    kind = field["type"]
    if kind == "boolean":
        return type(value) is bool
    if kind == "integer":
        return (type(value) is int and -(2**53 - 1) <= value <= 2**53 - 1
                and field.get("minimum", value) <= value <= field.get("maximum", value))
    if not isinstance(value, str) or not 0 < len(value) <= 4096:
        return False
    if kind == "enum":
        return any(option["value"] == value for option in field.get("options", []))
    fmt = field.get("format")
    if fmt == "hostname_label":
        return bool(re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", value))
    try:
        if fmt == "date":
            return date.fromisoformat(value).isoformat() == value
        if fmt == "timezone":
            ZoneInfo(value)
    except (ValueError, ZoneInfoNotFoundError):
        return False
    return True


def configuration(manifest, values):
    fields = {f["key"]: f for f in manifest["configuration"]}
    require(isinstance(values, dict) and set(values) <= fields.keys(), "invalid_configuration")
    result = {}
    missing = object()
    for key, field in fields.items():
        value = values.get(key, field.get("default", missing))
        if value is missing or value == "":
            require(not field["required"], "missing_configuration")
        else:
            require(_accepts(field, value), "invalid_configuration")
            result[key] = value
    return result


def _unique(values):
    require(len(values) == len(set(values)))


def _safe_strings(value, depth=0):
    require(depth <= 32)
    if isinstance(value, str):
        require(value not in {"constructor", "prototype", "__proto__"})
        require(not any(ord(c) < 32 or 0xD800 <= ord(c) <= 0xDFFF for c in value))
    elif isinstance(value, dict):
        for key, item in value.items():
            require(key.isascii())
            _safe_strings(item, depth + 1)
    elif isinstance(value, list):
        for item in value:
            _safe_strings(item, depth + 1)


def validate_manifest(raw: bytes, expected_digest: str | None = None):
    m = parse_json(raw, 128 * 1024)
    schema = json.loads(files(__package__).joinpath("manifest-v1.schema.json").read_text())
    require(Draft202012Validator(schema).is_valid(m))
    _safe_strings(m)
    require(m["id"].split("/")[0] == m["publisher"]["id"])
    for url in [m["documentation"], m["publisher"]["url"]]:
        u = urlsplit(url)
        require(u.scheme == "https" and u.hostname and not u.username and not u.password and not u.fragment)
    for values in [[f["key"] for f in m["configuration"]], [s["id"] for s in m["streams"]], [s["id"] for s in m["scopes"]]]:
        _unique(values)
    fields = {f["key"]: f for f in m["configuration"]}
    for f in fields.values():
        require(not f["secret"] or (f["type"] == "string" and "default" not in f and "format" not in f))
        require((f["type"] == "enum") == ("options" in f))
        require("format" not in f or f["type"] == "string")
        require(not ("minimum" in f or "maximum" in f) or f["type"] == "integer")
        require(f.get("minimum", float("-inf")) <= f.get("maximum", float("inf")))
        if "options" in f:
            _unique([o["value"] for o in f["options"]])
        require("default" not in f or _accepts(f, f["default"]))
    runtime = m["runtime"]
    if runtime["kind"] == "file":
        require(m["permissions"]["localFiles"] and not m["scopes"])
        _unique(runtime["formats"])
    else:
        require(not m["permissions"]["localFiles"])
        origin, auth = runtime["origin"], runtime["auth"]
        require(not re.search(r"(?:^|\.)(localhost|local|internal|arpa|invalid)$", origin["host"]))
        if "subdomainField" in origin:
            f = fields.get(origin["subdomainField"], {})
            require(f.get("required") and not f.get("secret") and f.get("format") == "hostname_label")
        refs = {"bearer": [("tokenField", True)], "header": [("valueField", True)],
                "basic": [("usernameField", False), ("passwordField", True)], "none": []}[auth["type"]]
        for name, secret in refs:
            f = fields.get(auth[name], {})
            require(f.get("required") and f.get("type") == "string" and f.get("secret") is secret)
        if runtime["kind"] == "reviewed_adapter":
            require(origin["host"] == "myshopify.com" and "subdomainField" in origin
                    and auth["type"] == "header" and auth["name"] == "X-Shopify-Access-Token")
    require(m["capabilities"]["incremental"] == any(s["sync"]["mode"] == "incremental" for s in m["streams"]))
    for stream in m["streams"]:
        _unique([c["key"] for c in stream["columns"]]); _unique(stream["primaryKey"])
        columns = {c["key"]: c for c in stream["columns"]}
        require(all(k in columns and not columns[k]["nullable"] for k in stream["primaryKey"]))
        sync = stream["sync"]
        if sync["mode"] == "incremental":
            cursor = columns.get(sync["cursorField"], {})
            require(cursor.get("nullable") is False and cursor.get("type") in {"timestamp", "string", "integer_string"})
            if sync["deletionPolicy"] == "explicit_tombstone":
                require(columns.get(sync.get("tombstoneField"), {}).get("type") == "boolean")
            else:
                require("tombstoneField" not in sync)
        require((runtime["kind"] == "rest") == ("request" in stream))
        if "request" in stream:
            req = stream["request"]
            require(not any(p in {".", ".."} for p in req["path"].split("/")))
            params = [p["name"] for p in req["parameters"]]
            paging = req["pagination"]
            if paging["type"] != "single":
                params += [paging["limitParam"], paging[{"offset": "offsetParam", "page": "pageParam", "cursor": "cursorParam"}[paging["type"]]]]
            if sync["mode"] == "incremental":
                params.append(sync["cursorParam"])
            _unique(params)
            for p in req["parameters"]:
                ref = p["value"]
                if ref["from"] == "config":
                    require(ref["field"] in fields and not fields[ref["field"]]["secret"])
    if expected_digest is not None:
        require(content_digest(m) == expected_digest, "manifest_digest_mismatch")
    return m
