#!/usr/bin/env python3
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


RESPONSES = {
    "/api/version": {"current": "synthetic", "latest": "synthetic"},
    "/api/services": [
        {
            "id": 100003,
            "networkId": 1,
            "serviceId": 3,
            "name": "Synthetic Service",
            "type": 1,
        }
    ],
    "/api/programs": [
        {
            "id": 10000300004,
            "networkId": 1,
            "serviceId": 3,
            "eventId": 4,
            "startAt": 4102444800000,
            "duration": 1800000,
            "isFree": True,
            "name": "Synthetic Program",
            "description": "",
        }
    ],
    "/api/tuners": [{}, {}],
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        response = RESPONSES.get(self.path)
        if response is None:
            self.send_error(404)
            return
        body = json.dumps(response, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        return


def main():
    if len(sys.argv) != 2 or not sys.argv[1].isdigit():
        raise SystemExit("usage: synthetic_mirakurun.py <port>")
    port = int(sys.argv[1])
    if port < 1024 or port > 65535:
        raise SystemExit("port must be between 1024 and 65535")
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
