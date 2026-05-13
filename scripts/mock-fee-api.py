#!/usr/bin/env python3
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main() -> None:
    parser = argparse.ArgumentParser(description="Serve a mempool.space-like fee API response.")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18081)
    parser.add_argument("--fastest-fee", type=int, default=9)
    parser.add_argument("--half-hour-fee", type=int, default=5)
    parser.add_argument("--hour-fee", type=int, default=3)
    parser.add_argument("--minimum-fee", type=int, default=1)
    args = parser.parse_args()

    payload = {
        "fastestFee": args.fastest_fee,
        "halfHourFee": args.half_hour_fee,
        "hourFee": args.hour_fee,
        "minimumFee": args.minimum_fee,
    }

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            if self.path == "/api/v1/fees/recommended":
                body = json.dumps(payload).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return
            if self.path == "/healthz":
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"ok")
                return
            self.send_error(404, "unknown path")

        def log_message(self, format: str, *args) -> None:
            return

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"mock fee api listening on http://{args.host}:{args.port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
