"""HTTPS transport for dlt. Network policy is independent of manifest validity."""
from dataclasses import dataclass
import http.client
import ipaddress
import socket
import ssl
import threading
import time
from urllib.parse import urlsplit

import requests

from .manifest import ConnectorError, require
from .client import SourceResponseMixin


class SourceResponse(SourceResponseMixin, requests.Response):
    pass


@dataclass(frozen=True)
class Budget:
    deadline: float
    cancelled: threading.Event

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        require(not self.cancelled.is_set(), "cancelled")
        require(remaining > 0, "deadline_exceeded")
        return remaining

    def wait(self, seconds):
        self.cancelled.wait(min(seconds, self.remaining()))
        self.remaining()


def public_addresses(host: str):
    try:
        addresses = socket.getaddrinfo(host, 443, type=socket.SOCK_STREAM)
    except OSError:
        raise ConnectorError("source_dns_failed") from None
    require(bool(addresses), "source_dns_failed")
    # Reject mixed public/private DNS answers as well as private-only answers.
    require(all(ipaddress.ip_address(address[4][0]).is_global for address in addresses), "network_denied")
    return addresses


class PublicHTTPSConnection(http.client.HTTPSConnection):
    def connect(self):
        # Connect to a verified numeric address directly. Do not resolve again
        # between validation and connection; TLS still authenticates the hostname.
        addresses = public_addresses(self.host)
        sock = None
        try:
            family, kind, proto, _, address = addresses[0]
            sock = socket.socket(family, kind, proto)
            sock.settimeout(self.timeout)
            sock.connect(address)
            self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        except (OSError, ssl.SSLError):
            if sock is not None:
                sock.close()
            raise ConnectorError("source_connect_failed") from None


class BoundedSession(requests.Session):
    """No proxies, cookies, redirects, environment credentials or URL pagination."""

    def __init__(self, host: str, limits: dict, budget: Budget):
        super().__init__()
        self.trust_env = False
        self.max_redirects = 0
        self.host, self.limits, self.budget = host, limits, budget
        self.headers.clear()
        self._ssl = ssl.create_default_context()

    def send(self, request, **kwargs):
        try:
            u = urlsplit(request.url)
            port = u.port
        except ValueError:
            raise ConnectorError("network_denied") from None
        require(u.scheme == "https" and u.hostname == self.host and port in (None, 443)
                and not u.username and not u.password and not u.fragment, "network_denied")
        require(request.method == "GET" and not request.body, "method_denied")
        require(len(request.url) <= 16384, "request_limit")
        allowed = {"authorization", "x-api-key", "x-shopify-access-token", "accept", "user-agent"}
        require(all(k.lower() in allowed for k in request.headers), "header_denied")
        require(all(isinstance(v, str) and "\r" not in v and "\n" not in v for v in request.headers.values()), "header_denied")
        for attempt in range(self.limits["retryAttempts"] + 1):
            response = self._once(request, u)
            if response.status_code not in {429, 500, 502, 503, 504}:
                require(200 <= response.status_code < 300, f"source_http_{response.status_code}")
                return response
            require(attempt < self.limits["retryAttempts"], "source_retry_exhausted")
            retry_after = response.headers.get("Retry-After", "")
            # Excessive Retry-After is a deferred retry, not permission to retry early.
            delay = int(retry_after) if retry_after.isdigit() else min(2**attempt, 30)
            require(delay <= 60, "source_retry_deferred")
            self.budget.wait(max(1, delay))
        raise ConnectorError("source_retry_exhausted")

    def _once(self, request, url):
        timeout = min(self.limits["timeoutSeconds"], self.budget.remaining())
        connection = PublicHTTPSConnection(self.host, timeout=timeout, context=self._ssl)
        try:
            headers = dict(request.headers)
            headers["Accept-Encoding"] = "identity"
            target = url.path or "/"
            if url.query:
                target += "?" + url.query
            connection.request("GET", target, headers=headers)
            raw = connection.getresponse()
            require(raw.getheader("Content-Encoding", "identity") == "identity", "source_encoding_unsupported")
            length = raw.getheader("Content-Length")
            require(length is None or (length.isdigit() and int(length) <= self.limits["responseBytes"]), "response_limit")
            data = bytearray()
            while True:
                self.budget.remaining()
                chunk = raw.read1(min(65536, self.limits["responseBytes"] + 1 - len(data)))
                if not chunk:
                    break
                data.extend(chunk)
                require(len(data) <= self.limits["responseBytes"], "response_limit")
            response = SourceResponse()
            response.status_code = raw.status
            response.headers = requests.structures.CaseInsensitiveDict(raw.getheaders())
            response._content = bytes(data)
            response.request, response.url = request, request.url
            return response
        except (OSError, http.client.HTTPException):
            # GET transport failures are resumable from the durable checkpoint;
            # provider URLs, headers and bodies never become public exceptions.
            raise ConnectorError("source_transport_failed") from None
        finally:
            connection.close()
