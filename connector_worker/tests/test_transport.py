import socket
import threading
import time
import unittest
from unittest.mock import patch

from requests import Request, Response

from syne_connectors.manifest import ConnectorError
from syne_connectors.transport import BoundedSession, Budget, public_addresses, PublicHTTPSConnection
from unittest.mock import MagicMock


class TransportTests(unittest.TestCase):
    def session(self):
        return BoundedSession("api.example.com", {"timeoutSeconds":5,"retryAttempts":2,"responseBytes":2048}, Budget(time.monotonic()+30, threading.Event()))

    def test_private_dns_and_mixed_answers_denied(self):
        for ip in ["127.0.0.1","10.0.0.1","169.254.169.254","::1","100.100.100.200","::ffff:127.0.0.1"]:
            with self.subTest(ip=ip), patch("socket.getaddrinfo",return_value=[(socket.AF_INET,socket.SOCK_STREAM,6,"",(ip,443))]):
                with self.assertRaises(ConnectorError): public_addresses("api.example.com")
        with patch("socket.getaddrinfo",return_value=[(socket.AF_INET,1,6,"",("8.8.8.8",443)),(socket.AF_INET,1,6,"",("10.0.0.1",443))]):
            with self.assertRaises(ConnectorError): public_addresses("api.example.com")

    def test_auth_cannot_leave_host_or_mutate(self):
        session = self.session()
        for method, url in [("GET","https://evil.example.com/"),("GET","http://api.example.com/"),("GET","https://user@api.example.com/"),("POST","https://api.example.com/")]:
            with self.subTest(url=url), patch.object(session,"_once") as send:
                with self.assertRaises(ConnectorError): session.send(Request(method,url).prepare())
                send.assert_not_called()

    def test_redirects_fail_and_retry_budget_is_bounded(self):
        session = self.session(); request = Request("GET","https://api.example.com/",headers={"Authorization":"Bearer secret"}).prepare()
        response = Response(); response.status_code = 302
        with patch.object(session,"_once",return_value=response) as send:
            with self.assertRaises(ConnectorError): session.send(request)
            self.assertEqual(send.call_count,1)
        response.status_code=429;response.headers["Retry-After"]="0"
        with patch.object(session,"_once",return_value=response) as send, patch.object(Budget,"wait"):
            with self.assertRaises(ConnectorError): session.send(request)
            self.assertEqual(send.call_count,3)
        response.headers["Retry-After"]="86400"
        with patch.object(session,"_once",return_value=response) as send:
            with self.assertRaisesRegex(ConnectorError,"source_retry_deferred"): session.send(request)
            self.assertEqual(send.call_count,1)

    def test_cancellation_stops_backoff(self):
        budget=Budget(time.monotonic()+30,threading.Event());budget.cancelled.set()
        with self.assertRaisesRegex(ConnectorError,"cancelled"): budget.wait(20)

    def test_verified_address_is_connected_without_second_dns_lookup(self):
        context=MagicMock(); sock=MagicMock()
        addresses=[(socket.AF_INET,socket.SOCK_STREAM,6,"",("8.8.8.8",443))]
        with patch("socket.getaddrinfo",return_value=addresses) as lookup, patch("socket.socket",return_value=sock):
            connection=PublicHTTPSConnection("api.example.com",timeout=2,context=context)
            connection.connect()
            self.assertEqual(lookup.call_count,1)
            sock.connect.assert_called_once_with(("8.8.8.8",443))
            context.wrap_socket.assert_called_once_with(sock,server_hostname="api.example.com")

    def test_response_overflow_and_compression_close_connection(self):
        request=Request("GET","https://api.example.com/").prepare()
        for length, encoding, chunks in [("4096","identity",[]),(None,"gzip",[]),(None,"identity",[b"x"*2049])]:
            raw=MagicMock(); raw.status=200
            raw.getheader.side_effect=lambda name, default=None: {"Content-Length":length,"Content-Encoding":encoding}.get(name,default)
            raw.read1.side_effect=chunks
            connection=MagicMock();connection.getresponse.return_value=raw
            with patch("syne_connectors.transport.PublicHTTPSConnection",return_value=connection):
                with self.assertRaises(ConnectorError): self.session().send(request)
                connection.close.assert_called_once()
