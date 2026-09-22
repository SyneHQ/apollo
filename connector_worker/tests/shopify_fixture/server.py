"""Loopback HTTP fixture. The acceptance gateway supplies worker-facing TLS."""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import socket
import stat
import threading
import time

from .provider import FixtureError, FixtureProvider, MAX_BODY


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *_args):
        pass

    def handle(self):
        def close():
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        timer = threading.Timer(10, close)
        timer.daemon = True
        timer.start()
        self.connection.settimeout(5)
        try:
            super().handle()
        except (OSError, ValueError):
            pass
        finally:
            timer.cancel()

    def do_POST(self):
        try:
            keys = [key.lower() for key in self.headers.keys()]
            if len(keys) > 20 or len(keys) != len(set(keys)) or self.headers.get("Transfer-Encoding"):
                raise FixtureError()
            length = self.headers.get("Content-Length", "")
            if not length.isdecimal() or int(length) > MAX_BODY:
                raise FixtureError(413)
            deadline, body = time.monotonic() + 5, bytearray()
            while len(body) < int(length):
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise FixtureError(408)
                self.connection.settimeout(remaining)
                chunk = self.rfile.read1(int(length) - len(body))
                if not chunk:
                    raise FixtureError()
                body.extend(chunk)
            status, headers, response = self.server.provider.respond("POST", self.path,
                {key.lower(): value for key, value in self.headers.items()}, bytes(body))
        except FixtureError as error:
            status, headers, response = error.status, {"Content-Type": "application/json"}, b'{"error":"fixture_request_rejected"}'
        self.send_response(status)
        for key, value in headers.items():
            self.send_header(key, value)
        if "Content-Length" not in headers:
            self.send_header("Content-Length", str(len(response)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(response)

    def send_error(self, code, message=None, explain=None):
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.send_header("Connection", "close")
        self.end_headers()


class Server(ThreadingHTTPServer):
    daemon_threads = True
    request_queue_size = 4

    def __init__(self, port, provider):
        self.provider = provider
        self.slots = threading.BoundedSemaphore(4)
        super().__init__(("127.0.0.1", port), Handler)

    def process_request(self, request, address):
        if not self.slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, address)
        except BaseException:
            self.slots.release()
            raise

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.slots.release()

    def handle_error(self, request, address):
        # HTTP errors must never dump incoming credentials or provider bodies.
        pass


def token_file(path):
    file = Path(path)
    if not file.is_absolute():
        raise FixtureError()
    descriptor = os.open(file, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_size > 129:
            raise FixtureError()
        return os.read(descriptor, 130).decode("ascii").strip()
    finally:
        os.close(descriptor)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--source-token-file", required=True)
    parser.add_argument("--control-token-file", required=True)
    args = parser.parse_args()
    try:
        if not 1 <= args.port <= 65535:
            raise FixtureError()
        provider = FixtureProvider(token_file(args.source_token_file), token_file(args.control_token_file))
        with Server(args.port, provider) as server:
            server.serve_forever()
    except (FixtureError, OSError, UnicodeError):
        parser.exit(1, "Shopify fixture unavailable; verify its local configuration.\n")


if __name__ == "__main__":
    main()
