# Phoebe backend

Go module `phoebe`. Binaries: `cmd/api` (HTTP) and `cmd/worker` (jobs + sweeps). Both are wired by `internal/app` (`APIDeps`, `RegisterWorker`), which the tests use too. Design: `../docs/backend.md`; HTTP contract: `../docs/api.md`; package APIs for contributors: `FOUNDATION.md`.

## Prerequisites

- Go 1.27, Docker (compose v2+), `jq`
- `ffmpeg` on PATH: worker with a real STT provider, `audio` Chunker tests, chunked-transcribe test, smoke script (`brew install ffmpeg`)
- `pdftotext` (poppler) on PATH: PDF export tests (`brew install poppler`)

Missing `DATABASE_URL`, `ffmpeg` or `pdftotext` fails the tests that need them (never skipped).

## Database port

Root `docker-compose.yml` maps Postgres to host port **55432** (host 5432 is commonly taken by a local Postgres). Override with `PHOEBE_PG_PORT=5432 docker compose up -d postgres` if 5432 is free. All commands below use 55432.

## Commands

```sh
# start db (from repo root; healthcheck: pg_isready)
docker compose up -d postgres

cd backend
export DATABASE_URL='postgres://phoebe:phoebe@localhost:55432/phoebe?sslmode=disable'

# vet + all tests (tests create/drop their own phoebe_test_<hex> databases)
go vet ./...
go test ./... -count=1

# one test
go test ./internal/app -run TestCrossAccountAllRoutes -count=1 -v

# end-to-end smoke: builds + starts api and worker on :18080 (fake providers, dev login),
# then curls the whole flow (login → meeting → 2-chunk WAV upload → auto/explicit transcription
# → notes_ready → MoM → edit → review → md/docx/pdf/whatsapp export + signed download → audio
# URL → delete). Prints SMOKE PASS; on failure keeps logs in the printed temp dir.
./scripts/smoke.sh                 # SMOKE_PORT=… to change the port
```

## Run locally (fake providers, dev login)

```sh
cd backend
export DATABASE_URL='postgres://phoebe:phoebe@localhost:55432/phoebe?sslmode=disable'
export HTTP_ADDR=:8080 PUBLIC_BASE_URL=http://localhost:8080 DEV_LOGIN_ENABLED=true
export SIGNING_SECRET=$(printf 'dev-signing-secret-not-for-prod!' | xxd -p -c 64)
export SUBJECT_PEPPER=$(printf 'dev-subject-pepper-not-for-prod!' | xxd -p -c 64)
export STORAGE_DIR=./data/storage            # api and worker must share it
export STT_PROVIDER=fake LLM_PROVIDER=fake
go run ./cmd/api                             # terminal 1
go run ./cmd/worker                          # terminal 2 (same env)
```

Everything in containers (postgres + api + worker, shared storage volume, dev login on, api on :8080):

```sh
docker compose up --build
```

## Dev login

```sh
curl -s -X POST localhost:8080/v1/auth/dev -H 'Content-Type: application/json' -d '{"subject":"alice"}'
# → {"token":"phb_…", …}; then: -H "Authorization: Bearer phb_…"
curl -s localhost:8080/healthz
curl -s localhost:8080/v1/config
```

## Worker

Job types: `transcribe`, `generate_notes`, `generate_mom`, `purge_meeting`, `purge_account`. Expired job leases are reclaimed by the claim query. Sweeps every `SWEEP_INTERVAL` (default 5m, also once at start), each logged as `sweep.run`:

| Sweep | Effect |
|---|---|
| `upload_expiry` | open sessions past `expires_at` with no live writer → `expired`, temp bytes deleted |
| `audio_retention` | transcribed audio past `retain_until` (no active transcribe) → deleted `expired`, bytes deleted |
| `byte_purge` | deleted audio whose byte delete crashed/failed (> 1 min ago) → bytes deleted, `bytes_purged_at` set |
| `orphans` | stored objects older than 24 h that no live upload session, audio asset or export references → deleted |
| `alerts` | `alert.queue_stuck` / `alert.cost_spike` |

## Local dev env (minimum)

| Var | Local value |
|---|---|
| `DATABASE_URL` | `postgres://phoebe:phoebe@localhost:55432/phoebe?sslmode=disable` (in compose: host `postgres:5432`) |
| `PUBLIC_BASE_URL` | `http://localhost:8080` (must be `https://` unless `DEV_LOGIN_ENABLED=true`) |
| `DEV_LOGIN_ENABLED` | `true` |
| `SIGNING_SECRET`, `SUBJECT_PEPPER` | any ≥ 32 bytes, hex or base64 (dev values above; never reuse in deploy) |
| `STORAGE_DIR` | `./data/storage` (api and worker must share it) |
| `STT_PROVIDER`, `LLM_PROVIDER` | `fake` (no API keys needed) |

All other variables and defaults: `docs/backend.md` §16. Real providers: `STT_PROVIDER=openai STT_API_KEY=… STT_MODEL=whisper-1`, `LLM_PROVIDER=openaicompat LLM_BASE_URL=… LLM_API_KEY=… LLM_MODEL=…`.
