from decimal import Decimal
import json
from pathlib import Path
import threading
import time
import unittest
from unittest.mock import patch

from dlt.sources.helpers.rest_client.paginators import SinglePagePaginator
from requests import Request

from syne_connectors.client import BoundedRESTClient
from syne_connectors.manifest import ConnectorError
from syne_connectors.transport import BoundedSession, Budget, SourceResponse


class DltClientTests(unittest.TestCase):
    def test_dlt_uses_bounded_transport_and_exact_source_decimals(self):
        session=BoundedSession("api.example.com", {"timeoutSeconds":5,"retryAttempts":0,"responseBytes":2048},Budget(time.monotonic()+30,threading.Event()))
        response=SourceResponse();response.status_code=200
        response._content=b'{"items":[{"id":"one","amount":123456789012345678.10}]}'
        client=BoundedRESTClient("api.example.com",session,["items"],100,paginator=SinglePagePaginator())
        with patch.object(session,"_once",return_value=response) as send:
            pages=list(client.paginate("/items"))
        self.assertEqual(len(pages),1)
        self.assertEqual(pages[0][0]["amount"],Decimal("123456789012345678.10"))
        self.assertEqual(send.call_count,1)
        self.assertEqual(send.call_args.args[0].method,"GET")

    def test_missing_selector_fails_instead_of_empty_success(self):
        response=SourceResponse();response._content=b'{"error":"private-error"}'
        client=object.__new__(BoundedRESTClient);client.selector=["items"];client.page_limit=100
        with self.assertRaisesRegex(ConnectorError,"source_schema_changed"):
            client.extract_response(response,"$")

    def test_provider_duplicate_keys_are_rejected(self):
        response=SourceResponse();response._content=b'{"amount":1,"amount":2}'
        with self.assertRaises(ConnectorError): response.json()
