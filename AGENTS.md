# AGENTS.md

cloud-sync: a single Go binary that watches local media dirs, uploads to OpenList (139yun), tracks per-file state, and serves an embedded Web UI + JSON API. User-facing docs are in Chinese (`README.md`, `cloud-sync/README.md`, `ARCHITECTURE.md`).

## Commands

The Go module lives in `cloud-sync/` (`module cloud-sync`, a single `main` package). **Run all Go commands from `cloud-sync/`, not the repo root.**

```bash
gofmt -l .                       # must print nothing
go vet ./...
go test -race -count=1 ./...     # CI order is gofmt -> vet -> test -> build
go test -run TestAPI_Retry -race ./...        # single test
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o cloud-sync .
```

- E2E smoke (repo root, needs `python3`, ~15–20s, uses ports 5244/18099 + `/tmp`): `./scripts/smoke.sh` (env mode), `./scripts/smoke-config.sh` (YAML mode). Both print `OK`. They install `EXIT` traps and use fixed ports/paths — don't run them in parallel.
- Local playground: `./local-dev/run.sh` (UI `:8099`, mock `:5244`), stop with `./local-dev/stop.sh`.
- Running the binary needs a config: `cloud-sync [config.yaml]` (default `/config/cloud-sync.yaml`). Loading is strict — unknown YAML keys and missing required values error; required: `OPENLIST_URL/TOKEN`, `OPENLIST_SRC/DST_STORAGE`, `LOG_LEVEL`, `ALLOWED_SOURCE_PREFIXES`, `WATCH_DIRS`, and the numeric knobs. `config.example.yaml` is the reference.

## Layout

- Entrypoint `cloud-sync/main.go`; wiring `supervisor.go` (task generation: watcher + pipeline + cleanup, start/pause/reload).
- `pipeline.go` state machine: stat → whitelist → size → key → inflight → `AlreadySynced` → stabilize → upload+retry → record. **Do not move the `AlreadySynced` check after `stabilize`** — that made restarts re-scan every file at the 30s stabilize window.
- `state.go` writes `<sync_status_dir>/<date>/<key>.json` (+ `FAILED/`). `AlreadySynced` is true for **any** status (synced/failed/cleaned), and retry deletes the record first. Default `sync_status_dir` is `/config/.sync_status`; if it isn't persistent, everything re-uploads.
- `openlist.go` targets OpenList v4: `POST /api/fs/copy` with `names[]` (not `src_name`/`dst_name`) + `skip_existing`/`overwrite`; async task polled via `POST /api/admin/task/copy/info`. Mock: `scripts/mock-openlist.py`.
- Web assets `cloud-sync/web/` are `//go:embed web`ed in `web.go` — editing `index.html`/`app.js`/`style.css` requires a rebuild; there is no live-reload server.

## Testing quirks

- Tests inject uploaders via `SupervisorDeps.NewUploader` (`mockUploader`); no real OpenList needed. `newTestSupervisor` sleeps ~50ms for the startup scan and registers `t.Cleanup(sup.Stop)`.
- `-race` matters: concurrency guards are deliberate (`Generation.mu`+`stopped` for `Enqueue`; `oneOffMu`/`shuttingDown` for `ProcessOne`). Keep the Add-under-lock-before-Wait pattern intact.
- Retry and cleanup are allowed while tasks are paused via a one-off pipeline/cleanup (`Supervisor.ProcessOne`, transient `Cleanup`). `Pause` must NOT set `shuttingDown` — only the final `Stop` does.

## Branch & release flow

- All development happens on `dev`. Push to `dev` → `dev.yml` runs gofmt/vet/test and pushes `:dev`, `:dev-<stamp>`, `:sha-<short>-dev`; the embedded version is `dev-<stamp>`.
- `rc.yml`/`release.yml` are tag-triggered and **fail unless the tag commit is an ancestor of `main`**. To cut one: merge `dev` into `main`, then `git tag v0.1.0-rcN && git push origin v0.1.0-rcN` (rc) or `v1.0.0` (release). `ci.yml` runs on push to `main` and PRs to `main`/`dev`.
- Deploys select the image with `CLOUD_SYNC_TAG` (default `latest`, which is stale — use `:dev` or an immutable `sha-…-dev`/`vX.Y.Z-rcN`). Compose uses `pull_policy: always`; `docker-compose.build.yml` builds a local `cloud-sync:dev` and needs `--build`.
- Image must stay < 30 MiB (distroless); CI enforces this, so avoid heavy new deps/embedded assets.

## References

- `config.example.yaml`, `.env.example` — config surface.
- `docs/superpowers/specs/` + `ARCHITECTURE.md` — design intent.
- Commit messages use Conventional Commits (see `git log --oneline`).
