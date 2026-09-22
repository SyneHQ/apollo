import io
import json
import ssl
import unittest
from unittest.mock import patch

from syne_connectors.bridge import BridgeSink, MAX_RESPONSE_BYTES
from syne_connectors.handoff import write_pages
from syne_connectors.manifest import ConnectorError
from syne_connectors.pages import Page
from syne_connectors.transport import Budget
from test_pages import budget, normal, Sink


class Response:
    def __init__(self, status=200, data=b"{}", headers=None):
        self.status, self.data, self.headers = status, io.BytesIO(data), headers or {}
    def getheader(self, key, default=None): return self.headers.get(key, default)
    def read1(self, n): return self.data.read(n)


class Connection:
    def __init__(self, response): self.response, self.requests, self.closed, self.sock = response, [], False, None
    def request(self, method, path, **kwargs): self.requests.append((method, path, kwargs))
    def getresponse(self): return self.response
    def close(self): self.closed = True


class BridgeTests(unittest.TestCase):
    def test_origin_tls_and_header_validation(self):
        for origin in ["http://bridge.example", "https://user:pass@bridge.example", "https://bridge.example/api",
                       "https://bridge.example?token=a", "https://bridge.example#x", "https://bridge.example:bad", "https://bridge.example\n"]:
            with self.assertRaises(ConnectorError): BridgeSink(origin, "token", budget())
        for token in ["", "a\r\nb", "x"*8193]:
            with self.assertRaises(ConnectorError): BridgeSink("https://bridge.example", token, budget())
        insecure = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT); insecure.check_hostname = False; insecure.verify_mode = ssl.CERT_NONE
        with self.assertRaisesRegex(ConnectorError, "bridge_tls_required"):
            BridgeSink("https://bridge.example", "token", budget(), tls_context=insecure)

    def test_exact_bytes_and_no_sql_overrides(self):
        payload = b'{"amount":"999999999999999.01"}'
        result = {"batch_id":"a", "sequence":1, "records":1, "digest":"a"*64}
        conn = Connection(Response(data=json.dumps(result).encode()))
        with patch("syne_connectors.bridge.http.client.HTTPSConnection", return_value=conn) as factory:
            self.assertEqual(BridgeSink("https://bridge.example:8443", "secret", budget()).commit(payload), result)
        self.assertEqual(factory.call_args.args, ("bridge.example", 8443))
        method, path, request = conn.requests[0]
        self.assertEqual((method,path), ("POST", "/api/v1/ingestion/commit"))
        self.assertIs(request["body"], payload)
        self.assertEqual(request["headers"]["X-Job-Token"], "secret")
        self.assertTrue(conn.closed)

    def test_redirect_errors_limits_and_invalid_state(self):
        for response, expected in [
            (Response(302, headers={"Location":"https://attacker.example"}), "destination_http_failed"),
            (Response(403, b"secret body"), "destination_grant_denied"),
            (Response(409, b"secret body"), "destination_conflict"),
            (Response(200, b"x"*(MAX_RESPONSE_BYTES+1)), "destination_response_limit"),
            (Response(200, headers={"Content-Length":str(MAX_RESPONSE_BYTES+1)}), "destination_response_limit"),
            (Response(200, headers={"Content-Encoding":"gzip"}), "destination_response_invalid"),
            (Response(200, b'{"sequence":0,"sequence":1}'), "duplicate_json_key"),
            (Response(200, b'{"sequence":true,"checkpoint":{}}'), "destination_response_invalid"),
            (Response(429, headers={"Retry-After":"900"}), "destination_retry_deferred"),
        ]:
            with self.subTest(expected=expected):
                conn = Connection(response)
                with patch("syne_connectors.bridge.http.client.HTTPSConnection", return_value=conn):
                    with self.assertRaisesRegex(ConnectorError, expected): BridgeSink("https://bridge.example", "secret", budget()).state()
                self.assertTrue(conn.closed)
                self.assertEqual(len(conn.requests), 1)

    def test_ambiguous_ack_retries_identical_post_and_cancellation_stops_io(self):
        calls, store = [], Sink()
        def request(_self, method, path, **kwargs):
            calls.append(kwargs["body"])
            receipt = store.commit(kwargs["body"])
            _self.response = Response(503 if len(calls)==1 else 200, json.dumps(receipt).encode())
        with patch.object(Connection,"request",request), patch("syne_connectors.bridge.http.client.HTTPSConnection", side_effect=lambda *a,**k:Connection(None)), patch.object(Budget,"wait"):
            sink=BridgeSink("https://bridge.example","secret",budget())
            page=Page([{"id":"one"}], {"position":0,"done":False}, {"position":1,"done":True})
            result=write_pages([page],sink,store.state,"run",normal,budget())
        self.assertEqual(result["sequence"],1);self.assertEqual(calls[0],calls[1]);self.assertEqual(len(store.rows),1)
        b=budget();b.cancelled.set()
        with patch("syne_connectors.bridge.http.client.HTTPSConnection") as factory:
            with self.assertRaisesRegex(ConnectorError,"cancelled"): BridgeSink("https://bridge.example","secret",b).state()
            factory.assert_not_called()
