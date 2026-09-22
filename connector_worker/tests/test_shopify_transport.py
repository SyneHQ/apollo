import json
import unittest
from unittest.mock import patch

import requests

from syne_connectors.manifest import ConnectorError
from syne_connectors.transport import BoundedSession, SourceResponse
from syne_connectors.shopify.queries import API_VERSION, ENDPOINT, QUERIES
from syne_connectors.shopify.transport import ShopifySession
from test_pages import budget

LIMITS={"timeoutSeconds":30,"responseBytes":2097152,"retryAttempts":3}


def prepared(operation="orders", variables=None, query=None, url=None):
    variables = variables if variables is not None else {"first":25,"after":None,"filter":"created_at:>=2026-09-01"}
    return requests.Request("POST",url or "https://merchant.myshopify.com"+ENDPOINT,
        json={"query":query or QUERIES[operation],"variables":variables},
        headers={"X-Shopify-Access-Token":"secret-canary","Content-Type":"application/json"}).prepare()


def response(data,status=200,version=API_VERSION):
    value=SourceResponse();value.status_code=status
    value._content=json.dumps(data).encode();value.headers["X-Shopify-API-Version"]=version
    return value


class ShopifyTransportTests(unittest.TestCase):
    def test_only_fixed_read_queries_and_bounded_variables_can_reach_network(self):
        for req in [
            prepared(query="mutation { orderCancel(id: 1) { userErrors { message } } }"),
            prepared(query="query { __schema { types { name } } }"),
            prepared(url="https://merchant.myshopify.com"+ENDPOINT+"?query=x"),
            prepared(url="https://other.myshopify.com"+ENDPOINT),
            prepared(url="https://merchant.myshopify.com/admin/api/2025-01/graphql.json"),
            prepared(variables={"first":101,"after":None,"filter":"x"}),
            prepared(variables={"first":True,"after":None,"filter":"x"}),
            prepared(variables={"first":25,"after":"x"*4097,"filter":"x"}),
            prepared(variables={"first":25,"after":None,"filter":"x","url":"https://elsewhere"}),
            prepared(operation="line_items",variables={"first":10,"after":None,"id":"gid://shopify/Customer/1"}),
        ]:
            with self.subTest(url=req.url),patch.object(BoundedSession,"_once") as network:
                with self.assertRaises(ConnectorError): ShopifySession("merchant.myshopify.com",LIMITS,budget()).send(req)
                network.assert_not_called()
        with patch.object(BoundedSession,"_once") as network:
            with self.assertRaisesRegex(ConnectorError,"method_denied"):BoundedSession("merchant.myshopify.com",LIMITS,budget()).send(prepared())
            network.assert_not_called()

    def test_valid_read_documents_share_public_https_transport_and_fixed_version(self):
        for name in QUERIES:
            values={} if name=="access" else {"first":25,"after":None,**({"filter":"created_at:>=2026-09-01"} if name in {"orders","order_ids","refunds"} else {"id":f"gid://shopify/{'Order' if name=='line_items' else 'Refund'}/123"})}
            with patch.object(BoundedSession,"_once",return_value=response({"data":{"ok":True}})) as network:
                result=ShopifySession("merchant.myshopify.com",LIMITS,budget()).send(prepared(operation=name,variables=values))
                self.assertTrue(result.json()["data"]["ok"]);self.assertEqual(network.call_count,1)
        with patch.object(BoundedSession,"_once",return_value=response({"data":{}},version="2026-10")):
            with self.assertRaisesRegex(ConnectorError,"shopify_api_version_changed"):ShopifySession("merchant.myshopify.com",LIMITS,budget()).send(prepared())

    def test_graphql_throttle_uses_the_existing_retry_budget_and_discards_partial_data(self):
        throttled=response({"data":{"orders":{"nodes":[{"id":"must-not-be-returned"}]}},"errors":[{"extensions":{"code":"THROTTLED"}}],
            "extensions":{"cost":{"requestedQueryCost":100,"throttleStatus":{"currentlyAvailable":0,"restoreRate":25}}}})
        with patch.object(BoundedSession,"_once",side_effect=[throttled,response({"data":{"orders":{"nodes":[]}}})]) as network:
            b=budget()
            with patch.object(type(b),"wait") as wait:
                result=ShopifySession("merchant.myshopify.com",LIMITS,b).send(prepared())
                wait.assert_called_once_with(4)
        self.assertEqual(network.call_count,2);self.assertEqual(result.json()["data"]["orders"]["nodes"],[])
        for body in [{"errors":[{"message":"secret-canary","extensions":{"code":"ACCESS_DENIED"}}]},
                     {"data":None}, {"errors":[{"extensions":{"code":"THROTTLED"}}],"extensions":{"cost":{"requestedQueryCost":1000,"throttleStatus":{"currentlyAvailable":0,"restoreRate":1}}}}]:
            with patch.object(BoundedSession,"_once",return_value=response(body)):
                with self.assertRaises(ConnectorError) as caught:ShopifySession("merchant.myshopify.com",LIMITS,budget()).send(prepared())
                self.assertNotIn("secret-canary",str(caught.exception))
