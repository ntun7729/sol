#!/usr/bin/env python3
import argparse
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

BLOCK = bytes(range(256)) * 256  # 64 KiB deterministic block


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        if parsed.path == "/blob":
            total = int(query.get("bytes", [str(128 * 1024 * 1024)])[0])
            self._send(total, 0.0)
            return
        if parsed.path == "/stream":
            mbps = float(query.get("mbps", ["8"])[0])
            seconds = float(query.get("seconds", ["15"])[0])
            bytes_per_second = mbps * 1_000_000 / 8.0
            total = int(bytes_per_second * seconds)
            self._send(total, bytes_per_second)
            return
        self.send_response(404)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _send(self, total, bytes_per_second):
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(total))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        sent = 0
        started = time.monotonic()
        while sent < total:
            chunk = BLOCK[: min(len(BLOCK), total - sent)]
            try:
                self.wfile.write(chunk)
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                return
            sent += len(chunk)
            if bytes_per_second:
                target = started + sent / bytes_per_second
                delay = target - time.monotonic()
                if delay > 0:
                    time.sleep(delay)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=19090)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
