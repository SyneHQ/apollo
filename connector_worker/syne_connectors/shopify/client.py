"""Strict dlt GraphQL pagination and explicit order-history access."""
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone

from dlt.sources.helpers.rest_client.paginators import JSONResponseCursorPaginator

from ..client import BoundedRESTClient
from ..manifest import require
from .queries import ENDPOINT, QUERIES


def at(value, keys):
    for key in keys:
        require(isinstance(value, dict) and key in value, "source_schema_changed")
        value = value[key]
    return value


class ShopifyCursor(JSONResponseCursorPaginator):
    def __init__(self, path, after):
        self.path = path
        self.seen = {after} if after else set()
        super().__init__(cursor_path=".".join(path + ["pageInfo", "endCursor"]),
                         cursor_body_path="variables.after",
                         has_more_path=".".join(path + ["pageInfo", "hasNextPage"]),
                         stop_after_empty_page=False)

    def update_state(self, response, data=None):
        # Validate before dlt can construct exceptions containing source bodies.
        info = at(response.json(), self.path + ["pageInfo"])
        require(isinstance(info, dict) and set(info) == {"hasNextPage", "endCursor"}, "source_cursor_invalid")
        more, cursor = info["hasNextPage"], info["endCursor"]
        require(type(more) is bool, "source_cursor_invalid")
        require(cursor is None or (isinstance(cursor, str) and 0 < len(cursor) <= 4096), "source_cursor_invalid")
        if more:
            require(cursor is not None and cursor not in self.seen, "source_cursor_loop")
            self.seen.add(cursor)
        super().update_state(response, data)


@dataclass
class ConnectionPage:
    records: list[dict]
    after: str | None
    more: bool
    response: dict


class ShopifyClient:
    def __init__(self, session, token, budget):
        self.session, self.budget = session, budget
        self.headers = {"X-Shopify-Access-Token": token, "Content-Type": "application/json"}
        self.requests = 0

    def tick(self):
        self.budget.remaining()
        self.requests += 1
        require(self.requests <= 10000, "source_page_budget")

    def check_access(self, start_date, now):
        self.tick()
        client = BoundedRESTClient(self.session.host, self.session, [], 1, headers=self.headers)
        data = client.post(ENDPOINT, json={"query": QUERIES["access"], "variables": {}}).json()
        scopes = at(data, ["data", "currentAppInstallation", "accessScopes"])
        require(isinstance(scopes, list) and len(scopes) <= 1000, "source_scope_invalid")
        require(all(isinstance(item, dict) and isinstance(item.get("handle"), str) for item in scopes), "source_scope_invalid")
        names = {item["handle"] for item in scopes}
        require("read_orders" in names, "shopify_read_orders_required")
        start = datetime.fromisoformat(start_date).replace(tzinfo=timezone.utc)
        require(start <= now, "shopify_history_in_future")
        require(start >= now - timedelta(days=60) or "read_all_orders" in names, "shopify_history_scope_required")

    def pages(self, operation, variables, path, *, count=0):
        pager = ShopifyCursor(path, variables["after"])
        client = BoundedRESTClient(self.session.host, self.session, path + ["nodes"],
                                  variables["first"], paginator=pager, headers=self.headers)
        iterator = client.paginate(ENDPOINT, method="POST", json={"query": QUERIES[operation], "variables": dict(variables)})
        try:
            while True:
                self.tick()
                try:
                    page = next(iterator)
                except StopIteration:
                    return
                count += len(page)
                require(count <= 25000, "shopify_pagination_limit")
                yield ConnectionPage(list(page), page.request.json["variables"]["after"],
                                     page.paginator.has_next_page, page.response.json())
        finally:
            iterator.close()
