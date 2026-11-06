# Phoebe backend

Go module `phoebe`. Binaries: `cmd/api` (HTTP) and `cmd/worker` (jobs + sweeps). Both are wired by `internal/app` (`APIDeps`, `RegisterWorker`), which the tests use too. Design: `../docs/backend.md`; HTTP contract: `../docs/api.md`; package APIs for contributors: `FOUNDATION.md`.

## Prerequisites

- Go 1.27, Docker (compose v2+), `jq`
- `ffmpeg` on PATH: worker with a real STT provider (silence detection + chunking), `audio` and `ai/openai` tests, chunked-transcribe test, smoke script (`brew install ffmpeg`)
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
# with notes auto → notes_ready (chunk-timed transcript, approximate refs) → MoM → edit → review
# → md/docx/pdf/whatsapp export ([\~mm:ss] refs + legend in md) + signed download → audio URL →
# usage incl. ai_generations → transcription only (notes skip) → transcript_ready → generate
# notes → delete). Prints SMOKE PASS; on failure keeps logs in the printed temp dir.
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
# → {"access_token":"…","refresh_token":"…", …}; then: -H "Authorization: Bearer <access_token>"
# access token lasts ACCESS_TOKEN_TTL (15m); renew: POST /v1/auth/refresh {"refresh_token":"…"}
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

All other variables and defaults: `docs/backend.md` §16.

## Real providers

Fake providers stay the default for dev and tests. Real ones are adapters behind `internal/ai` (design: `docs/backend.md` §6.1); keys come only from env.

```sh
# STT: OpenAI (worker needs ffmpeg)
export STT_PROVIDER=openai STT_API_KEY=sk-…          # STT_MODEL defaults to gpt-4o-mini-transcribe
# LLM: DeepSeek (OpenAI-compatible chat completions, JSON output, thinking off)
export LLM_PROVIDER=deepseek LLM_API_KEY=sk-…        # LLM_BASE_URL=https://api.deepseek.com, LLM_MODEL=deepseek-flash by default
# LLM: any other OpenAI-compatible endpoint (LLM_BASE_URL required, no default)
export LLM_PROVIDER=openaicompat LLM_API_KEY=… LLM_BASE_URL=https://… LLM_MODEL=…
```

| Var | Default | Notes |
|---|---|---|
| `STT_MODEL` | `gpt-4o-mini-transcribe` | `gpt-4o-mini-transcribe`, `gpt-4o-transcribe` → timing `chunk` (approximate, our ~30 s windows); `whisper-1` → timing `segment` (provider segment timestamps). Other values fail worker startup. |
| `STT_CHUNK_TARGET_SECONDS` / `_MIN_` / `_MAX_` | `30` / `10` / `45` | chunk timing: cut at the silence midpoint nearest the target, hard cut at max when there is no silence; validated `min ≤ target ≤ max`, `max ≥ 2 × min` |
| `STT_SILENCE_NOISE_DB`, `STT_SILENCE_MIN_DURATION` | `-35`, `500ms` | ffmpeg `silencedetect` over the whole file (both timings) |
| `STT_DROP_SILENCE_RATIO` | `0.8` | segment timing (whisper-1) only: a low-confidence segment is dropped only when silence also covers this much of its span; otherwise kept with `uncertain=true`. Chunk timing keeps all low-confidence text, flagged uncertain; it skips (no call, not billed) only windows whose non-silent audio is shorter than `STT_SILENCE_MIN_DURATION` |
| `STT_UNCERTAIN_MEAN_LOGPROB` | `-0.7` | gpt-4o models: mean token logprob below this = low confidence |
| `STT_UNCERTAIN_AVG_LOGPROB`, `STT_UNCERTAIN_NO_SPEECH_PROB`, `STT_UNCERTAIN_COMPRESSION_RATIO` | `-1.0`, `0.6`, `2.4` | whisper-1: any one crossed = low confidence |
| `STT_MAX_FILE_BYTES`, `STT_CHUNK_SECONDS` | `24000000`, `600` | whisper-1 only: larger files are cut into fixed pieces |
| `LLM_PROVIDER` | `fake` | `deepseek` (sends DeepSeek's `thinking` param), `openaicompat` (any other OpenAI-compatible endpoint, no `thinking` param) |
| `LLM_THINKING_NOTES`, `LLM_THINKING_MOM` | `false`, `false` | DeepSeek thinking mode per task (`{"thinking":{"type":"enabled"\|"disabled"}}`; DeepSeek's own default is enabled, so we always send it) |
| `STT_PRICE_PER_MIN`, `LLM_PRICE_IN_PER_MTOK`, `LLM_PRICE_OUT_PER_MTOK` | `0` | set at deploy from the provider price list (cost tracking only) |

DeepSeek `deepseek-flash` (DeepSeek-V4.1-Flash) list prices, USD per 1M tokens, from <https://api-docs.deepseek.com/quick_start/pricing/> (checked 2026-11-06; not defaults, verify before setting):

| | Off-peak | Peak |
|---|---|---|
| Input, cache hit | 0.003 | 0.006 |
| Input, cache miss | 0.15 | 0.30 |
| Output | 0.60 | 1.20 |

Peak is 01:00–04:00 and 06:00–10:00 UTC, Monday to Friday (excluding Chinese public holidays); everything else is off-peak. `LLM_PRICE_*` takes one figure per direction, so cost tracking uses whichever rate you set (the peak cache-miss price is the conservative choice).

### Live provider checks (opt-in, real API calls)

```sh
OPENAI_API_KEY=sk-… DEEPSEEK_API_KEY=sk-… ./scripts/live-check.sh
```

Runs `TestLiveOpenAISTT` (`internal/ai/openai`, Indonesian speech fixture `testdata/speech_id.m4a` made with macOS `say -v Damayanti`; asserts non-empty text, timing `chunk` and monotonic approximate bounds) and `TestLiveDeepSeekNotesJSON` (`internal/ai/openaicompat`, notes JSON from a tiny transcript checked against the notes output schema, thinking off and on). Both need `PHOEBE_LIVE=1` plus their key; otherwise they print `SKIPPED …` naming the env vars, so `go test ./...` never calls a provider.
