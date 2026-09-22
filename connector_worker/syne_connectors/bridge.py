"""Bounded first-party HTTPS handoff; never a general SQL or source client."""
import http.client
import ssl
from urllib.parse import urlsplit

from .handoff import MAX_BATCH_BYTES
from .manifest import ConnectorError, parse_json, require
from .pages import source_bytes

MAX_RESPONSE_BYTES = 32 << 10


class BridgeSink:
    """origin and token come from authorized bootstrap, never source configuration.

    Internal/private service addresses are valid here; source HTTP uses the
    separate public-only transport. The caller owns the shared deadline/retries.
    """

    def __init__(self, origin, token, budget, *, tls_context=None):
        require(isinstance(origin, str) and len(origin) <= 2048, "bridge_origin_invalid")
        try:
            url = urlsplit(origin)
            port = url.port or 443
        except ValueError:
            raise ConnectorError("bridge_origin_invalid") from None
        require(url.scheme == "https" and url.hostname and not url.username and not url.password
                and url.path in ("", "/") and not url.query and not url.fragment
                and not any(ord(c) < 33 for c in origin), "bridge_origin_invalid")
        require(isinstance(token, str) and 0 < len(token) <= 8192
                and all(32 < ord(c) < 127 for c in token), "bridge_token_invalid")
        self._context = tls_context or ssl.create_default_context()
        require(self._context.check_hostname and self._context.verify_mode == ssl.CERT_REQUIRED,
                "bridge_tls_required")
        self._host, self._port, self._token, self.budget = url.hostname, port, token, budget

    def install(self):
        result = self._call("install", b"{}")
        require(result == {"installed": True}, "destination_response_invalid")

    def state(self):
        result = self._call("state", b"{}")
        require(type(result.get("sequence")) is int and 0 <= result["sequence"] < 2**63-1
                and isinstance(result.get("checkpoint"), dict), "destination_response_invalid")
        require(len(source_bytes(result["checkpoint"])) <= 16 << 10, "destination_response_invalid")
        return result

    def commit(self, raw):
        require(isinstance(raw, bytes) and 0 < len(raw) <= MAX_BATCH_BYTES, "batch_limit")
        result = self._call("commit", raw)
        require(type(result.get("sequence")) is int and type(result.get("records")) is int
                and isinstance(result.get("batch_id"), str) and isinstance(result.get("digest"), str),
                "destination_response_invalid")
        # write_pages verifies exact ID, count, next sequence and bytes digest.
        return result

    def _call(self, operation, body):
        require(operation in {"install", "state", "commit"}, "bridge_operation_invalid")
        connection = http.client.HTTPSConnection(self._host, self._port,
            timeout=min(10, self.budget.remaining()), context=self._context)
        try:
            connection.request("POST", "/api/v1/ingestion/" + operation, body=body,
                headers={"X-Job-Token": self._token, "Content-Type": "application/json",
                         "Accept": "application/json", "Accept-Encoding": "identity"})
            raw = connection.getresponse()
            # Never follow redirects or include upstream error bodies in errors.
            if raw.status in {401, 403}:
                raise ConnectorError("destination_grant_denied")
            if raw.status == 409:
                raise ConnectorError("destination_conflict")
            if raw.status in {429, 500, 502, 503, 504}:
                delay = raw.getheader("Retry-After", "")
                if delay:
                    require(delay.isdigit() and int(delay) <= 30, "destination_retry_deferred")
                    self.budget.wait(int(delay))
                raise ConnectionError("destination_ack_unknown")
            require(raw.status == 200, "destination_http_failed")
            require(raw.getheader("Content-Encoding", "identity") == "identity", "destination_response_invalid")
            length = raw.getheader("Content-Length")
            require(length is None or (length.isdigit() and int(length) <= MAX_RESPONSE_BYTES), "destination_response_limit")
            data = bytearray()
            while True:
                self.budget.remaining()
                if connection.sock is not None:
                    connection.sock.settimeout(min(10, self.budget.remaining()))
                chunk = raw.read1(min(8192, MAX_RESPONSE_BYTES + 1 - len(data)))
                if not chunk:
                    break
                data.extend(chunk)
                require(len(data) <= MAX_RESPONSE_BYTES, "destination_response_limit")
            result = parse_json(bytes(data), MAX_RESPONSE_BYTES)
            require(isinstance(result, dict), "destination_response_invalid")
            return result
        except ssl.SSLError:
            raise ConnectorError("destination_tls_failed") from None
        except (OSError, http.client.HTTPException):
            # A POST may already have committed. handoff retries identical bytes,
            # and failure after its bounded budget leaves the checkpoint intact.
            raise ConnectionError("destination_ack_unknown") from None
        finally:
            connection.close()
