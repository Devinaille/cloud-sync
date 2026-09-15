#!/usr/bin/env python3
"""
Local OpenList mock for cloud-sync smoke tests.

Implements the three endpoints cloud-sync actually uses:
  POST /api/fs/list             — Ping uses this; returns an empty directory listing.
  POST /api/fs/copy             — synchronously copies the source file to the
                                  destination path under the configured local
                                  mount, then returns a fake task_id.
  GET  /api/admin/task/<id>/done — returns status: "succeeded" for every task,
                                  so cloud-sync's poll loop finishes immediately.

Mappings between OpenList storage paths and local directories are passed via
environment variables so the mock can run with the same OPENLIST_*_STORAGE
values the real OpenList uses (no test-specific rewrites in cloud-sync).

  OPENLIST_LOCAL_SRC_DIR — local directory backing /local_media  (required)
  OPENLIST_LOCAL_DST_DIR — local directory backing /139yun_media (required)
  MOCK_OPENLIST_PORT     — listen port (default 5244)

Usage:

  OPENLIST_LOCAL_SRC_DIR=/tmp/ws/media \
  OPENLIST_LOCAL_DST_DIR=/tmp/ws/cloud \
  python3 cloud-sync/scripts/mock-openlist.py
"""
import http.server
import json
import os
import shutil
import sys
import uuid

PORT = int(os.environ.get("MOCK_OPENLIST_PORT", "5244"))
SRC_MOUNT = "/local_media"
DST_MOUNT = "/139yun_media"
SRC_LOCAL = os.environ.get("OPENLIST_LOCAL_SRC_DIR", "")
DST_LOCAL = os.environ.get("OPENLIST_LOCAL_DST_DIR", "")


def _check_local():
    if not SRC_LOCAL or not DST_LOCAL:
        sys.stderr.write(
            "mock-openlist: set OPENLIST_LOCAL_SRC_DIR and OPENLIST_LOCAL_DST_DIR\n"
        )
        sys.exit(2)


def _resolve(storage: str, name: str) -> str:
    """Map (storage_root, relative_name) → absolute local path.

    Refuses to escape the configured root (defensive — the cloud-sync client
    always sends names that come from its own config, but the mock must not
    trust them blindly).
    """
    if storage == SRC_MOUNT:
        root = os.path.abspath(SRC_LOCAL)
    elif storage == DST_MOUNT:
        root = os.path.abspath(DST_LOCAL)
    else:
        raise ValueError(f"unknown storage root: {storage!r}")
    full = os.path.abspath(os.path.join(root, name))
    if not (full == root or full.startswith(root + os.sep)):
        raise ValueError(f"path escapes mount: {full}")
    return full


class Handler(http.server.BaseHTTPRequestHandler):
    # Silence default request logging; cloud-sync is chatty enough already.
    def log_message(self, fmt, *args):
        pass

    def _send(self, code: int, obj):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self):
        n = int(self.headers.get("Content-Length", "0") or 0)
        return json.loads(self.rfile.read(n).decode("utf-8")) if n else {}

    def do_POST(self):
        if self.path == "/api/fs/list":
            # Ping path — return an empty listing with code 200.
            return self._send(
                200, {"code": 200, "message": "ok", "data": {"content": []}}
            )
        if self.path == "/api/fs/copy":
            req = self._read_json()
            try:
                src = _resolve(req["src_dir"], req["src_name"])
                dst = _resolve(req["dst_dir"], req["dst_name"])
            except (KeyError, ValueError) as e:
                return self._send(200, {"code": 400, "message": f"bad request: {e}"})
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            try:
                shutil.copy2(src, dst)
            except FileNotFoundError:
                return self._send(
                    200, {"code": 500, "message": f"source not found: {src}"}
                )
            except OSError as e:
                return self._send(200, {"code": 500, "message": str(e)})
            return self._send(
                200,
                {
                    "code": 200,
                    "message": "ok",
                    "data": {"task_id": uuid.uuid4().hex[:12]},
                },
            )
        return self._send(404, {"code": 404, "message": "not found"})

    def do_GET(self):
        prefix = "/api/admin/task/"
        suffix = "/done"
        if self.path.startswith(prefix) and self.path.endswith(suffix):
            return self._send(
                200,
                {"code": 200, "message": "ok", "data": {"status": "succeeded"}},
            )
        return self._send(404, {"code": 404, "message": "not found"})


def main():
    _check_local()
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(
        f"mock-openlist: {SRC_MOUNT}->{SRC_LOCAL}  {DST_MOUNT}->{DST_LOCAL}  "
        f"port={PORT}",
        flush=True,
    )
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
