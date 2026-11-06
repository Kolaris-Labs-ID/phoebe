#!/usr/bin/env bash
# End-to-end smoke test: builds and starts api + worker (fake providers, dev login), then drives
# the full flow with curl. Needs Postgres (docker compose up -d postgres), jq, ffmpeg, shasum.
set -euo pipefail

cd "$(dirname "$0")/.."
PORT=${SMOKE_PORT:-18080}
BASE="http://localhost:$PORT"
WORK=$(mktemp -d)
export DATABASE_URL=${DATABASE_URL:-'postgres://phoebe:phoebe@localhost:55432/phoebe?sslmode=disable'}
export HTTP_ADDR=":$PORT" PUBLIC_BASE_URL="$BASE" DEV_LOGIN_ENABLED=true
export SIGNING_SECRET=$(printf 'dev-signing-secret-not-for-prod!' | xxd -p -c 64)
export SUBJECT_PEPPER=$(printf 'dev-subject-pepper-not-for-prod!' | xxd -p -c 64)
export STORAGE_DIR="$WORK/storage" STT_PROVIDER=fake LLM_PROVIDER=fake
export WORKER_POLL_INTERVAL=200ms WORKER_ID=smoke LOG_LEVEL=info

pids=()
cleanup() {
  for p in "${pids[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [ "${SMOKE_OK:-}" = 1 ]; then rm -rf "$WORK"; else echo "logs kept in $WORK" >&2; fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; echo "--- last body:" >&2; cat "$WORK/body" >&2 2>/dev/null || true; echo >&2; exit 1; }
step() { echo "== $*"; }

go build -o "$WORK/api" ./cmd/api
go build -o "$WORK/worker" ./cmd/worker
"$WORK/api" >"$WORK/api.log" 2>&1 & pids+=($!)
"$WORK/worker" >"$WORK/worker.log" 2>&1 & pids+=($!)

for _ in $(seq 1 50); do curl -fs "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fs "$BASE/healthz" >/dev/null || fail "api did not become healthy (see $WORK/api.log)"

TOKEN=""
# req METHOD PATH [BODY] [extra curl args...] → sets STATUS, body in $WORK/body
req() {
  local method=$1 path=$2 body=${3:-}
  shift $(( $# < 3 ? $# : 3 ))
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$method" "$BASE$path")
  [ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer $TOKEN")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data "$body")
  STATUS=$(curl "${args[@]}" "$@")
}
expect() { [ "$STATUS" = "$1" ] || fail "${2:-request}: expected $1, got $STATUS"; }
j() { jq -r "$1" "$WORK/body"; }
uuid() { uuidgen | tr 'A-Z' 'a-z'; }
public() { local t=$TOKEN; TOKEN=""; req "$@"; TOKEN=$t; }

step "dev login"
req POST /v1/auth/dev "{\"subject\":\"smoke-$(date +%s)\"}"
expect 200 "dev login"
TOKEN=$(j .token)

step "create meeting"
MID=$(uuid)
req POST /v1/meetings "{\"id\":\"$MID\",\"title\":\"Smoke weekly sync\",\"timezone\":\"Asia/Jakarta\",\"started_at\":\"2026-11-06T02:00:00Z\",\"language\":\"id\",\"participants\":[\"Budi\",\"Sari\"]}"
expect 201 "create meeting"
req POST /v1/meetings "{\"id\":\"$MID\",\"timezone\":\"UTC\"}"
expect 200 "create replay"

step "generate 60 s WAV"
ffmpeg -loglevel error -f lavfi -i "sine=frequency=440:duration=60" -ac 1 -ar 8000 -c:a pcm_s16le "$WORK/a.wav"
SIZE=$(wc -c <"$WORK/a.wav" | tr -d ' ')
SHA=$(shasum -a 256 "$WORK/a.wav" | cut -d' ' -f1)
HALF=$((SIZE / 2))
head -c "$HALF" "$WORK/a.wav" >"$WORK/c1"
tail -c "+$((HALF + 1))" "$WORK/a.wav" >"$WORK/c2"

step "upload session with auto transcription"
TKEY=$(uuid)
req POST "/v1/meetings/$MID/uploads" "{\"size_bytes\":$SIZE,\"sha256\":\"$SHA\",\"content_type\":\"audio/x-wav\",\"duration_ms\":60000,\"transcription\":{\"idempotency_key\":\"$TKEY\",\"language\":\"id\"}}"
expect 201 "create upload"
UPID=$(j .upload.id)

step "PUT chunk 1/2"
req PUT "/v1/uploads/$UPID" "" -H 'Content-Type: application/octet-stream' -H "Upload-Offset: 0" --data-binary "@$WORK/c1"
expect 200 "chunk 1"
[ "$(j .upload.received_bytes)" = "$HALF" ] || fail "received_bytes after chunk 1"

step "PUT chunk 2/2 (verifies + auto-starts transcription)"
req PUT "/v1/uploads/$UPID" "" -H 'Content-Type: application/octet-stream' -H "Upload-Offset: $HALF" --data-binary "@$WORK/c2"
expect 200 "chunk 2"
[ "$(j .upload.status)" = completed ] || fail "upload not completed"
[ "$(j .audio.content_type)" = audio/wav ] || fail "detected content type"
JOB=$(j .job.id)
[ "$JOB" != null ] || fail "auto-start job missing"

step "complete (idempotent confirm)"
req POST "/v1/uploads/$UPID/complete"
expect 200 "complete"
[ "$(j .job.id)" = "$JOB" ] || fail "complete returned another job"

step "explicit transcription with the auto-start key (replay)"
req POST "/v1/meetings/$MID/transcription" '{"language":"id"}' -H "Idempotency-Key: $TKEY"
expect 200 "transcription replay"
[ "$(j .job.id)" = "$JOB" ] || fail "replay returned another job"

poll() { # poll JQ-EXPR WANT FAILVALUE
  for _ in $(seq 1 150); do
    req GET "/v1/meetings/$MID"
    expect 200 "get meeting"
    local v; v=$(j "$1")
    [ "$v" = "$2" ] && return 0
    [ "$v" = "$3" ] && fail "state $v"
    sleep 0.2
  done
  fail "timeout waiting for $1 = $2"
}

step "poll until notes_ready"
poll .meeting.pipeline_state notes_ready notes_failed
[ "$(j .meeting.transcript.current_revision_number)" = 1 ] || fail "transcript missing"
[ "$(j .meeting.documents.ai_notes.current_revision_number)" = 1 ] || fail "ai notes missing"
req GET "/v1/meetings/$MID/transcript"
expect 200 "transcript"
[ "$(j '.revision.segments | length')" -ge 1 ] || fail "no segments"

step "generate MoM"
req POST "/v1/meetings/$MID/documents/mom/generate" '{"ai_notes_revision_id":null,"language":null}' -H "Idempotency-Key: $(uuid)"
expect 202 "mom generate"
poll .meeting.jobs.generate_mom.status succeeded failed

step "edit MoM with expected_revision"
req GET "/v1/meetings/$MID/documents/mom"
expect 200 "get mom"
N=$(j .revision.revision_number)
CONTENT=$(jq -c '.revision.content | .summary = "Ringkasan disunting di smoke test."' "$WORK/body")
req PUT "/v1/meetings/$MID/documents/mom" "{\"expected_revision\":$N,\"base_revision\":$N,\"mutation_id\":\"$(uuid)\",\"content\":$CONTENT}"
expect 201 "mom edit"
N2=$(j .revision.revision_number)
[ "$N2" = $((N + 1)) ] || fail "revision number"
req PUT "/v1/meetings/$MID/documents/mom" "{\"expected_revision\":$N,\"mutation_id\":\"$(uuid)\",\"content\":$CONTENT}"
expect 409 "stale edit"
[ "$(j .error.code)" = revision_conflict ] || fail "conflict code"

step "review MoM"
req PUT "/v1/meetings/$MID/documents/mom/review" "{\"status\":\"reviewed\",\"expected_revision\":$N2}"
expect 200 "review"
[ "$(j .document.review_status)" = reviewed ] || fail "review status"
req GET "/v1/meetings/$MID/documents/mom"
REV=$(j .revision.id)

step "export md / docx / pdf / whatsapp + signed download"
for f in md docx pdf whatsapp; do
  req POST /v1/exports "{\"document_revision_id\":\"$REV\",\"format\":\"$f\"}"
  expect 201 "export $f"
  [ "$(j .export.is_draft)" = false ] || fail "$f exported as draft"
  URL=$(j .download_url); BYTES=$(j .export.size_bytes); NAME=$(j .export.filename)
  case $f in md|whatsapp) [ "$(j .text)" != null ] || fail "$f text missing" ;; esac
  public GET "${URL#"$BASE"}" "" -D "$WORK/headers"
  expect 200 "download $f"
  [ "$(wc -c <"$WORK/body" | tr -d ' ')" = "$BYTES" ] || fail "$f size"
  grep -qi '^content-disposition: attachment' "$WORK/headers" || fail "$f disposition"
  case $f in
    pdf) head -c 4 "$WORK/body" | grep -q '%PDF' || fail "pdf magic" ;;
    docx) head -c 2 "$WORK/body" | grep -q 'PK' || fail "docx magic" ;;
  esac
  echo "   $f ok: $NAME ($BYTES bytes)"
  EXPORT_URL=$URL
done

step "audio playback URL"
req GET "/v1/meetings/$MID/audio/url"
expect 200 "audio url"
AURL=$(j .url)
public GET "${AURL#"$BASE"}" "" -H 'Range: bytes=0-43'
expect 206 "audio range"
public GET "${AURL#"$BASE"}"
expect 200 "audio download"
cmp -s "$WORK/body" "$WORK/a.wav" || fail "audio bytes differ"
public GET "${AURL#"$BASE"}x"
expect 403 "tampered url"

step "usage"
req GET /v1/usage
expect 200 "usage"
[ "$(j .consumed_seconds)" = 60 ] || fail "consumed_seconds"

step "delete meeting"
req DELETE "/v1/meetings/$MID"
expect 202 "delete"
req GET "/v1/meetings/$MID"
expect 404 "deleted meeting"
public GET "${EXPORT_URL#"$BASE"}"
expect 404 "export after delete"
req POST /v1/meetings "{\"id\":\"$MID\",\"timezone\":\"UTC\"}"
expect 409 "recreate deleted id"

SMOKE_OK=1
echo "SMOKE PASS"
