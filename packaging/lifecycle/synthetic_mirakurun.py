#!/usr/bin/env python3
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


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


def mpeg_crc32(data: bytes) -> int:
    crc = 0xFFFFFFFF
    for value in data:
        crc ^= value << 24
        for _ in range(8):
            crc = ((crc << 1) ^ 0x04C11DB7) & 0xFFFFFFFF if crc & 0x80000000 else (crc << 1) & 0xFFFFFFFF
    return crc


def make_pat() -> bytes:
    section = bytearray((0x00, 0xB0, 0x0D, 0x00, 0x02, 0xC1, 0x00, 0x00, 0x00, 0x03, 0xE1, 0x00))
    section.extend(mpeg_crc32(section).to_bytes(4, "big"))
    packet = bytes((0x47, 0x40, 0x00, 0x10, 0x00)) + bytes(section)
    pat_packet = packet + bytes((0xFF,)) * (188 - len(packet))
    null_packet = bytes((0x47, 0x1F, 0xFF, 0x10)) + bytes((0xFF,)) * 184
    return pat_packet + null_packet * 4


STREAM_PAT = make_pat()


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        request = urlsplit(self.path)
        if request.path == "/api/services/100003/stream":
            if request.query != "decode=0":
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "video/MP2T")
            self.send_header("Content-Length", str(len(STREAM_PAT)))
            self.end_headers()
            self.wfile.write(STREAM_PAT)
            return

        response = RESPONSES.get(request.path)
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
