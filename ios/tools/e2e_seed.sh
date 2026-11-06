#!/usr/bin/env bash
# Prepares the E2E UI tests (PhoebeUITests/E2EFlowTests) against the real backend on localhost:8080.
#   tools/e2e_seed.sh <simulator-udid> <restore-subject> [blocked-subject]
# 1. Copies a 20 s m4a as RapatUjiE2E.m4a into the simulator's Files "On My iPhone" (import tests).
# 2. Seeds <restore-subject> with "Rapat dari server" (uploaded, notes_ready, MoM drafted) for testRestoreFromServer,
#    and marks its transcript segment s2 uncertain (low_confidence) via SQL, as the real STT adapter would.
# 3. Optional: fills all AI generation slots of <blocked-subject> via SQL (30 consumed now) for testAISlotChoice.
# Needs jq, ffmpeg, uuidgen, shasum; api + worker running with fake providers and DEV_LOGIN_ENABLED=true;
# SQL goes through the root docker compose postgres (override with PHOEBE_PSQL, e.g. "psql $DATABASE_URL").
set -euo pipefail
DEV=$1 SUBJECT=$2 BLOCKED=${3:-}
B=${PHOEBE_API:-http://localhost:8080}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PSQL=${PHOEBE_PSQL:-docker compose -f $ROOT/docker-compose.yml exec -T postgres psql -U phoebe -d phoebe}
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
uuid() { uuidgen | tr 'A-Z' 'a-z'; }
sql() { $PSQL -v ON_ERROR_STOP=1 -qtA -c "$1"; }
login() { curl -fs -X POST "$B/v1/auth/dev" -H 'Content-Type: application/json' -d "{\"subject\":\"$1\"}"; }

FILES=$(xcrun simctl get_app_container "$DEV" com.apple.DocumentsApp groups | awk '/FileProvider.LocalStorage/ {print $2}')
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:duration=20" -ac 1 -ar 44100 -c:a aac -b:a 64k "$W/import.m4a"
cp "$W/import.m4a" "$FILES/File Provider Storage/RapatUjiE2E.m4a"

T=$(login "$SUBJECT" | jq -r .access_token)
H=(-H "Authorization: Bearer $T")
MID=$(uuid)
curl -fs "${H[@]}" -X POST "$B/v1/meetings" -H 'Content-Type: application/json' \
  -d "{\"id\":\"$MID\",\"title\":\"Rapat dari server\",\"timezone\":\"Asia/Jakarta\",\"started_at\":\"2026-11-05T02:00:00Z\",\"language\":\"id\",\"participants\":[\"Budi\",\"Sari\"]}" >/dev/null
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=330:duration=30" -ac 1 -ar 44100 -c:a aac -b:a 64k "$W/a.m4a"
SIZE=$(wc -c <"$W/a.m4a" | tr -d ' ')
SHA=$(shasum -a 256 "$W/a.m4a" | cut -d' ' -f1)
UP=$(curl -fs "${H[@]}" -X POST "$B/v1/meetings/$MID/uploads" -H 'Content-Type: application/json' \
  -d "{\"size_bytes\":$SIZE,\"sha256\":\"$SHA\",\"content_type\":\"audio/mp4\",\"duration_ms\":30000,\"transcription\":{\"idempotency_key\":\"$(uuid)\",\"language\":\"id\",\"notes\":\"auto\"}}" | jq -r .upload.id)
curl -fs "${H[@]}" -X PUT "$B/v1/uploads/$UP" -H 'Content-Type: application/octet-stream' -H 'Upload-Offset: 0' --data-binary "@$W/a.m4a" >/dev/null
for _ in $(seq 1 100); do
  [ "$(curl -fs "${H[@]}" "$B/v1/meetings/$MID" | jq -r .meeting.pipeline_state)" = notes_ready ] && break
  sleep 0.3
done
sql "UPDATE transcript_revisions SET segments = jsonb_set(jsonb_set(segments, '{1,uncertain}', 'true'), '{1,uncertain_reason}', '\"low_confidence\"') WHERE meeting_id = '$MID'" >/dev/null
curl -fs "${H[@]}" -X POST "$B/v1/meetings/$MID/documents/mom/generate" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuid)" -d '{"ai_notes_revision_id":null,"language":null}' >/dev/null
for _ in $(seq 1 100); do
  s=$(curl -fs "${H[@]}" "$B/v1/meetings/$MID" | jq -r .meeting.jobs.generate_mom.status)
  [ "$s" = succeeded ] && break
  sleep 0.3
done
[ "$s" = succeeded ] || { echo "MoM generation did not finish" >&2; exit 1; }
echo "seeded $SUBJECT meeting $MID"

if [ -n "$BLOCKED" ]; then
  UID_=$(login "$BLOCKED" | jq -r .user.id)
  LIMIT=$(curl -fs -H "Authorization: Bearer $(login "$BLOCKED" | jq -r .access_token)" "$B/v1/usage" | jq -r .ai_generations.limit)
  sql "INSERT INTO ai_generation_slots (user_id, status, reserved_at, consumed_at) SELECT '$UID_', 'consumed', now(), now() FROM generate_series(1, $LIMIT)" >/dev/null
  echo "filled $LIMIT AI generation slots of $BLOCKED"
fi
