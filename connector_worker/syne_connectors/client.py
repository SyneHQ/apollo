"""Use dlt pagination with bounded transport and fail-closed source selection."""
from decimal import Decimal
import json

from dlt.sources.helpers.rest_client import RESTClient

from .manifest import ConnectorError, _pairs, require


class SourceResponseMixin:
    def json(self, **kwargs):
        try:
            return json.loads(self.content.decode("utf-8"), object_pairs_hook=_pairs,
                              parse_float=Decimal,
                              parse_constant=lambda _: require(False, "invalid_source_json"))
        except (ValueError, UnicodeError, RecursionError):
            raise ConnectorError("invalid_source_json") from None


class BoundedRESTClient(RESTClient):
    def _log_request(self, request, prepared_url):
        # dlt's DEBUG logger includes headers and source query parameters.
        # Only sanitized progress counters are emitted by the worker runner.
        pass

    def extract_response(self, response, data_selector):
        # The Syne manifest permits key arrays, not arbitrary JSONPath programs.
        data = response.json()
        for key in self.selector:
            require(isinstance(data, dict) and key in data, "source_schema_changed")
            data = data[key]
        require(isinstance(data, list), "source_schema_changed")
        require(len(data) <= self.page_limit, "source_page_limit")
        require(all(isinstance(record, dict) for record in data), "source_schema_changed")
        return data

    def __init__(self, host, session, selector, page_limit, **kwargs):
        self.selector, self.page_limit = selector, page_limit
        super().__init__(base_url="https://"+host, session=session, data_selector="$", **kwargs)
