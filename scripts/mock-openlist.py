#!/usr/bin/env python3
"""
Local OpenList mock for cloud-sync smoke tests.

Implements the endpoints cloud-sync actually uses against real OpenList v4:

  POST /api/fs/list                                  — Ping: empty directory listing.
  POST /api/fs/copy                                  — copy via names[] + skip_existing/overwrite;
                                                       returns data.tasks[] (TaskInfo array).
  POST /api/admin/task/copy/info?tid=<id>           — task status lookup by numeric `state`.

All incoming request bodies are appended to ${MOCK_LOG_PATH} (when set) as
compact JSON, so smoke scripts can grep the captured request shape. When
MOCK_LOG_PATH is unset, request logging is disabled.

Mappings between OpenList storage paths and local directories are passed via
environment variables so the mock can run with the same OPENLIST_*_STORAGE
values the real OpenList uses (no test-specific rewrites in cloud-sync).

  OPENLIST_LOCAL_SRC_DIR — local directory backing /local_media  (required)
  OPENLIST_LOCAL_DST_DIR — local directory backing /139yun_media (required)
  MOCK_LOG_PATH          — append captured requests here (optional)
  MOCK_OPENLIST_PORT     — listen port (default 5244)

Usage:

  OPENLIST_LOCAL_SRC_DIR=/tmp/ws \
  OPENLIST_LOCAL_DST_DIR=/tmp/ws/cloud \
  python3 cloud-sync/scripts/mock-openlist.py
"""
import http.server
import json
import os
import shutil
import sys
import time
import urllib.parse

PORT = int(os.environ.get("MOCK_OPENLIST_PORT", "5244"))
SRC_MOUNT = "/local_media"
DST_MOUNT = "/139yun_media"
SRC_LOCAL = os.environ.get("OPENLIST_LOCAL_SRC_DIR", "")
DST_LOCAL = os.environ.get("OPENLIST_LOCAL_DST_DIR", "")

# Pending async-copy bookkeeping: task_id -> (state, status, error).
_TASKS = {}
_TASK_COUNTER = {"n": 0}


def _check_local():
    if not SRC_LOCAL or not DST_LOCAL:
        sys.stderr.write(
            "mock-openlist: set OPENLIST_LOCAL_SRC_DIR and OPENLIST_LOCAL_DST_DIR\n"
        )
        sys.exit(2)


def _resolve(storage: str, name: str) -> str:
    """Map (storage_root_or_subdir, name) → absolute local path.

    cloud-sync's pipeline.computeKey concatenates the storage root + the
    fixed "media" subdir + the parent directory of the file, then passes
    that as `src_dir`/`dst_dir` to OpenList with the file basename in
    `names`. So storage here can be the bare mount (`/local_media`), the
    `media` subdir (`/local_media/media`), or any deeper prefix under
    storage (`/local_media/media/Movies`). We strip a known prefix and
    use the remainder plus `name` to build the local path. Refuses to
    escape the configured root (defensive).
    """
    storage = storage.strip("/")
    for mount, local in ((SRC_MOUNT.strip("/"), SRC_LOCAL),
                          (DST_MOUNT.strip("/"), DST_LOCAL)):
        root = os.path.abspath(local)
        if storage == mount:
            rel = ""
        elif storage.startswith(mount + "/"):
            rel = storage[len(mount) + 1:]
        else:
            continue
        full = os.path.abspath(os.path.join(root, rel, name))
        if not (full == root or full.startswith(root + os.sep)):
            raise ValueError(f"path escapes mount: {full}")
        return full
    raise ValueError(f"unknown storage root: {storage!r}")


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _log_request(line: str) -> None:
    """Append a request line to ${MOCK_LOG_PATH} so smoke scripts can grep it.

    If MOCK_LOG_PATH is unset, logs are silently dropped. Smoke scripts pass
    an explicit path so they can assert on captured requests.
    """
    path = os.environ.get("MOCK_LOG_PATH", "")
    if not path:
        return
    try:
        with open(path, "a", encoding="utf-8") as f:
            f.write(line + "\n")
    except OSError:
        pass


def _new_task(state: int, status: str, error: str = "", total_bytes: int = 0) -> str:
    _TASK_COUNTER["n"] += 1
    tid = f"task-{_TASK_COUNTER['n']}"
    _TASKS[tid] = {
        "state": state,
        "status": status,
        "error": error,
        "total_bytes": total_bytes,
    }
    return tid


def _task_info(tid: str) -> dict:
    t = _TASKS.get(tid)
    if t is None:
        # Unknown task — default to succeeded (smoke tests don't exercise failure paths).
        return {
            "id": tid, "name": "copy", "creator": "", "creator_role": 0,
            "state": 2, "status": "succeeded", "progress": 100.0,
            "start_time": _now_iso(), "end_time": _now_iso(),
            "total_bytes": 0, "error": "",
        }
    now = _now_iso()
    return {
        "id": tid, "name": "copy", "creator": "", "creator_role": 0,
        "state": t["state"], "status": t["status"], "progress": 100.0,
        "start_time": now, "end_time": now,
        "total_bytes": t["total_bytes"], "error": t["error"],
    }


class Handler(http.server.BaseHTTPRequestHandler):
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
            return self._send(200, {"code": 200, "message": "ok", "data": {"content": []}})

        if self.path == "/api/fs/copy":
            req = self._read_json()
            _log_request(
                f"{_now_iso()} POST /api/fs/copy "
                f"{json.dumps(req, sort_keys=True, separators=(',', ':'))}"
            )

            names = req.get("names")
            if not isinstance(names, list) or len(names) == 0 or not all(names):
                # Real OpenList rejects empty or single-empty-element names arrays.
                return self._send(200, {"code": 400, "message": "Empty file names"})

            src_dir = req.get("src_dir", "")
            dst_dir = req.get("dst_dir", "")
            overwrite = bool(req.get("overwrite", False))
            skip_existing = bool(req.get("skip_existing", False))

            try:
                src = _resolve(src_dir, names[0])
                base = os.path.basename(src)
                dst = _resolve(dst_dir, base)
            except (ValueError, KeyError) as e:
                return self._send(200, {"code": 400, "message": f"bad request: {e}"})

            if os.path.exists(dst):
                if not overwrite and not skip_existing:
                    return self._send(
                        200,
                        {"code": 403, "message": f"file [{base}] exists", "data": None},
                    )
                if skip_existing:
                    # Real OpenList skips silently; no task created.
                    return self._send(
                        200,
                        {"code": 200, "message": "skipped",
                         "data": {"message": "skipped", "tasks": []}},
                    )

            try:
                os.makedirs(os.path.dirname(dst), exist_ok=True)
                if not os.path.exists(src):
                    return self._send(
                        200, {"code": 500, "message": f"source not found: {src}"}
                    )
                shutil.copy2(src, dst)
            except OSError as e:
                return self._send(200, {"code": 500, "message": str(e)})

            total = os.path.getsize(dst)
            tid = _new_task(state=2, status="succeeded", total_bytes=total)
            return self._send(
                200,
                {
                    "code": 200,
                    "message": f"created 1 task(s)",
                    "data": {
                        "message": "ok",
                        "tasks": [_task_info(tid)],
                    },
                },
            )

        # POST /api/admin/task/copy/info?tid=...  (querystring)
        if self.path.startswith("/api/admin/task/copy/info"):
            qs = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
            tid = (qs.get("tid") or [""])[0]
            _log_request(f"{_now_iso()} POST /api/admin/task/copy/info tid={tid}")
            return self._send(200, {"code": 200, "message": "ok", "data": _task_info(tid)})

        return self._send(404, {"code": 404, "message": "not found"})

    def do_GET(self):
        # cloud-sync only uses POST endpoints; anything else is a 404.
        return self._send(404, {"code": 404, "message": "not found"})


def main():
    _check_local()
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print(
        f"mock-openlist: {SRC_MOUNT}->{SRC_LOCAL}  {DST_MOUNT}->{DST_LOCAL}  port={PORT}",
        flush=True,
    )
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
