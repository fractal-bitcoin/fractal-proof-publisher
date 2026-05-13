#!/usr/bin/env python3
import argparse
import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def load_state_map(path: Path) -> dict[str, dict[str, str]]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, dict):
        raise ValueError("state map must be a JSON object")
    state_map: dict[str, dict[str, str]] = {}
    for key, value in raw.items():
        if not isinstance(value, dict):
            raise ValueError(f"height {key} must map to an object")
        blockhash = str(value.get("blockhash", "")).strip()
        statehash = str(value.get("statehash", "")).strip()
        if not blockhash or not statehash:
            raise ValueError(f"height {key} must contain blockhash and statehash")
        state_map[str(key)] = {"blockhash": blockhash, "statehash": statehash}
    return state_map


def generated_payload(height: str) -> dict[str, str]:
    blockhash = hashlib.sha256(f"mock-blockhash:{height}".encode("utf-8")).hexdigest()
    statehash = hashlib.sha256(f"mock-statehash:{height}".encode("utf-8")).hexdigest()
    return {"blockhash": blockhash, "statehash": statehash}


def main() -> None:
    parser = argparse.ArgumentParser(description="Serve the height -> {blockhash,statehash} API expected by the publisher.")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--data", help="Optional JSON override file such as {'123': {'blockhash': '...', 'statehash': '...'}}")
    args = parser.parse_args()

    data_path = Path(args.data) if args.data else None

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            if self.path == "/healthz":
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"ok")
                return

            height = self.path.lstrip("/")
            if not height.isdigit():
                self.send_error(400, f"height must be numeric, got {height!r}")
                return

            payload = None
            if data_path is not None:
                try:
                    state_map = load_state_map(data_path)
                except Exception as exc:
                    self.send_error(500, f"failed to load state map: {exc}")
                    return
                payload = state_map.get(height)
            if payload is None:
                payload = generated_payload(height)

            body = json.dumps(payload).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, format: str, *args) -> None:
            return

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    if data_path is None:
        print(f"mock state api listening on http://{args.host}:{args.port} using deterministic generated payloads", flush=True)
    else:
        print(f"mock state api listening on http://{args.host}:{args.port} using overrides from {data_path}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
