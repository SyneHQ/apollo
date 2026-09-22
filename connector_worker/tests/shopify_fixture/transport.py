"""Test-image substitution at the provider network boundary only."""
import http.client
import os
import ssl
from urllib.parse import urlsplit

from syne_connectors.manifest import ConnectorError, require
from syne_connectors.shopify.queries import ENDPOINT
from syne_connectors.transport import BoundedSession, SourceResponse
from .provider import SOURCE_HOST, SOURCE_PATH, MAX_BODY, MAX_RESPONSE


def fixture_once(session, request, url):
    require(session.host == SOURCE_HOST and url.scheme == "https" and url.hostname == SOURCE_HOST
            and url.port in (None, 443) and url.path == ENDPOINT and not url.query
            and not url.fragment and not url.username and not url.password
            and request.method == "POST" and isinstance(request.body, bytes) and len(request.body) <= MAX_BODY,
            "fixture_source_denied")
    origin = os.environ.get("CONNECTOR_BOOTSTRAP_ORIGIN", "")
    ca = os.environ.get("CONNECTOR_SERVICE_CA_PEM", "")
    try:
        target = urlsplit(origin)
        require(target.scheme == "https" and bool(target.hostname) and target.path in ("", "/")
                and not target.username and not target.password and not target.query and not target.fragment
                and len(origin) <= 2048 and 1 <= (target.port or 443) <= 65535
                and bool(ca) and len(ca) <= 65536, "fixture_transport_unconfigured")
        context = ssl.create_default_context(cadata=ca)
    except (ValueError, ssl.SSLError):
        raise ConnectorError("fixture_transport_unconfigured") from None
    connection = http.client.HTTPSConnection(target.hostname, target.port or 443,
        timeout=min(session.limits["timeoutSeconds"], session.budget.remaining()), context=context)
    try:
        connection.request("POST", SOURCE_PATH, body=request.body, headers={
            "Content-Type": "application/json", "Accept-Encoding": "identity",
            "X-Shopify-Access-Token": request.headers.get("X-Shopify-Access-Token", "")})
        raw = connection.getresponse()
        require(raw.getheader("Content-Encoding", "identity") == "identity", "source_encoding_unsupported")
        maximum = min(MAX_RESPONSE, session.limits["responseBytes"])
        length = raw.getheader("Content-Length")
        require(length is not None and length.isdigit() and int(length) <= maximum, "response_limit")
        data = bytearray()
        while True:
            remaining = session.budget.remaining()
            if connection.sock is not None:
                connection.sock.settimeout(min(session.limits["timeoutSeconds"], remaining))
            chunk = raw.read1(min(65536, maximum + 1 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
            require(len(data) <= maximum, "response_limit")
        require(len(data) == int(length), "fixture_response_incomplete")
        response = SourceResponse()
        response.status_code, response._content = raw.status, bytes(data)
        response.headers.update(raw.getheaders())
        response.request, response.url = request, request.url
        return response
    except (OSError, http.client.HTTPException):
        raise ConnectorError("source_transport_failed") from None
    finally:
        connection.close()


def install():
    # Deliberately deny every other source in this fixture-only image.
    BoundedSession._once = fixture_once
