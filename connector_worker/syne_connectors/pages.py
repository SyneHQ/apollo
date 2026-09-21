"""dlt extraction pages with stable, durable continuation coordinates."""
from dataclasses import dataclass
from hashlib import sha256

from dlt.sources.helpers.rest_client.paginators import (
    JSONResponseCursorPaginator, OffsetPaginator, PageNumberPaginator, SinglePagePaginator,
)
from requests.auth import HTTPBasicAuth
import simplejson

from .client import BoundedRESTClient
from .manifest import configuration, require
from .transport import BoundedSession


def source_bytes(value):
    return simplejson.dumps(value, ensure_ascii=False, separators=(",", ":"),
                            use_decimal=True, allow_nan=False).encode("utf-8")


@dataclass
class Page:
    records: list[dict]
    start: dict
    after: dict

    @property
    def digest(self):
        return sha256(source_bytes(self.records)).hexdigest()


class StrictCursor(JSONResponseCursorPaginator):
    def __init__(self, paging):
        self.keys = paging["nextCursorPath"]
        super().__init__(cursor_path=".".join(self.keys), cursor_param=paging["cursorParam"])

    def update_state(self, response, data=None):
        value = response.json()
        for key in self.keys:
            require(isinstance(value, dict) and key in value, "source_cursor_missing")
            value = value[key]
        require(value is None or (isinstance(value, str) and len(value) <= 4096), "source_cursor_invalid")
        super().update_state(response, data)


def paginator(paging, position):
    kind = paging["type"]
    if kind == "single":
        require(position is None, "checkpoint_invalid")
        return SinglePagePaginator(), None
    if kind == "cursor":
        require(position is None or (isinstance(position, str) and len(position) <= 4096), "checkpoint_invalid")
        return StrictCursor(paging), paging["cursorParam"]
    require(type(position) is int and 0 <= position <= 2**53-1, "checkpoint_invalid")
    if kind == "offset":
        return OffsetPaginator(limit=paging["pageSize"], offset=position, offset_param=paging["offsetParam"], limit_param=paging["limitParam"], total_path=None), paging["offsetParam"]
    return PageNumberPaginator(base_page=paging["firstPage"], page=position, page_param=paging["pageParam"], total_path=None), paging["pageParam"]


def rest_pages(manifest, stream, values, checkpoint, budget, *, session_factory=BoundedSession):
    require(manifest["runtime"]["kind"] == "rest", "runtime_unsupported")
    require(manifest["id"] != "syne/razorpay" or manifest["version"] == "1.0.1", "connector_upgrade_required")
    require(stream["sync"]["mode"] == "snapshot", "incremental_adapter_required")
    values = configuration(manifest, values)
    runtime = manifest["runtime"]; origin = runtime["origin"]
    host = ((values[origin["subdomainField"]]+".") if "subdomainField" in origin else "") + origin["host"]
    req, headers = stream["request"], {}
    auth, auth_spec = None, runtime["auth"]
    if auth_spec["type"] == "basic":
        auth = HTTPBasicAuth(values[auth_spec["usernameField"]], values[auth_spec["passwordField"]])
    elif auth_spec["type"] == "bearer":
        headers["Authorization"] = "Bearer " + values[auth_spec["tokenField"]]
    elif auth_spec["type"] == "header":
        headers[auth_spec["name"]] = values[auth_spec["valueField"]]
    params = {}
    for p in req["parameters"]:
        ref = p["value"]
        if ref["from"] == "literal":
            params[p["name"]] = ref["value"]
        elif ref["field"] in values:
            params[p["name"]] = values[ref["field"]]
    paging = req["pagination"]
    initial = 0 if paging["type"] == "offset" else paging.get("firstPage")
    # A complete snapshot starts a fresh scan; an interrupted scan resumes.
    current = {"position": initial, "done": False} if not checkpoint or checkpoint.get("done") is True else dict(checkpoint)
    require(set(current) <= {"position", "done", "page_hash", "row_offset"} and current.get("done") is False and "position" in current, "checkpoint_invalid")
    pager, parameter = paginator(paging, current["position"])
    if paging["type"] != "single":
        params[paging["limitParam"]] = paging["pageSize"]
    if parameter and current["position"] is not None:
        params[parameter] = current["position"]
    seen = set()
    with session_factory(host, manifest["limits"], budget) as session:
        client = BoundedRESTClient(host, session, req["recordsPath"], paging.get("pageSize", 1000), paginator=pager, auth=auth, headers=headers)
        for index, page in enumerate(client.paginate(req["path"], params=params)):
            budget.remaining()
            require(index < 10000, "source_page_budget")
            done = not page.paginator.has_next_page
            next_position = page.request.params.get(parameter) if parameter else None
            after = {"position": next_position, "done": done}
            if not done:
                key = sha256(source_bytes(next_position)).hexdigest()
                require(next_position != current["position"] and key not in seen, "source_cursor_loop")
                seen.add(key)
            yield Page(list(page), current, after)
            current = after
