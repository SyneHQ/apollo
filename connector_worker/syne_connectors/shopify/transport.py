"""Allowlisted GraphQL POSTs over the existing public-only dlt transport."""
from decimal import Decimal
import math
import re

from ..manifest import parse_json, require
from ..transport import BoundedSession
from .queries import API_VERSION, ENDPOINT, QUERIES


class ShopifySession(BoundedSession):
    def __init__(self, host, limits, budget):
        require(re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.myshopify\.com", host), "network_denied")
        super().__init__(host, limits, budget)

    def validate_method(self, request, url):
        require(request.method == "POST" and url.path == ENDPOINT and not url.query, "method_denied")
        require(isinstance(request.body, bytes) and len(request.body) <= 32768, "request_limit")
        require(request.headers.get("Content-Type") == "application/json", "header_denied")
        data = parse_json(request.body, 32768)
        require(isinstance(data, dict) and set(data) == {"query", "variables"}, "graphql_operation_denied")
        operation = next((name for name, text in QUERIES.items() if text == data["query"]), None)
        require(operation is not None, "graphql_operation_denied")
        values = data["variables"]
        keys = set() if operation == "access" else {"first", "after", "filter" if operation in {"orders", "order_ids", "refunds"} else "id"}
        require(isinstance(values, dict) and set(values) == keys, "graphql_variables_invalid")
        if not keys:
            return
        require(type(values["first"]) is int and 1 <= values["first"] <= 100, "graphql_variables_invalid")
        require(values["after"] is None or (isinstance(values["after"], str) and 0 < len(values["after"]) <= 4096), "graphql_variables_invalid")
        if "filter" in keys:
            require(isinstance(values["filter"], str) and 0 < len(values["filter"]) <= 1024, "graphql_variables_invalid")
        else:
            entity = "Order" if operation == "line_items" else "Refund"
            require(isinstance(values["id"], str) and len(values["id"]) <= 128 and re.fullmatch(f"gid://shopify/{entity}/[0-9]+", values["id"]), "graphql_variables_invalid")

    def allowed_headers(self):
        return {"x-shopify-access-token", "accept", "user-agent", "content-type", "content-length"}

    def _once(self, request, url):
        response = super()._once(request, url)
        if response.status_code != 200:
            return response
        require(response.headers.get("X-Shopify-API-Version") == API_VERSION, "shopify_api_version_changed")
        data = response.json()
        require(isinstance(data, dict), "source_schema_changed")
        errors = data.get("errors")
        if errors:
            require(isinstance(errors, list) and all(isinstance(error, dict) and isinstance(error.get("extensions"), dict)
                    and error["extensions"].get("code") == "THROTTLED" for error in errors), "shopify_graphql_failed")
            extensions = data.get("extensions", {})
            require(isinstance(extensions, dict), "source_schema_changed")
            cost = extensions.get("cost", {})
            require(isinstance(cost, dict), "source_schema_changed")
            throttle = cost.get("throttleStatus", {})
            require(isinstance(throttle, dict), "source_schema_changed")
            requested, available, rate = cost.get("requestedQueryCost"), throttle.get("currentlyAvailable"), throttle.get("restoreRate")
            def numeric(value):
                return type(value) is int or (type(value) is Decimal and value.is_finite())
            if all(numeric(value) for value in [requested, available, rate]) and 0 <= requested <= 1000 and 0 <= available <= 10000 and 0 < rate <= 10000:
                delay = max(1, math.ceil((requested - available) / rate))
            else:
                delay = 2
            require(delay <= 60, "source_retry_deferred")
            response.status_code = 429
            response.headers["Retry-After"] = str(delay)
        else:
            require(isinstance(data.get("data"), dict), "source_schema_changed")
        return response
