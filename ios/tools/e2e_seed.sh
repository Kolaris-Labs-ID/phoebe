#!/usr/bin/env bash
# Prepares the E2E UI tests (PhoebeUITests/E2EFlowTests) against the real backend on localhost:8080.
#   tools/e2e_seed.sh <simulator-udid> <restore-subject>
# 1. Copies a 20 s m4a as RapatUjiE2E.m4a into the simulator's Files "On My iPhone" (import test).
# 2. Seeds <restore-subject> with "Rapat dari server" (uploaded, notes_ready, MoM drafted) for testRestoreFromServer.
# Needs jq, ffmpeg, uuidgen, shasum; api + worker running with fake providers and DEV_LOGIN_ENABLED=true.
set -euo pipefail
DEV=$1 SUBJECT=$2
B=${PHOEBE_API:-http://localhost:8080}
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
uuid() { uuidgen | tr 'A-Z' 'a-z'; }

FILES=$(xcrun simctl get_app_container "$DEV" com.apple.DocumentsApp groups | awk '/FileProvider.LocalStorage/ {print $2}')
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:duration=20" -ac 1 -ar 44100 -c:a aac -b:a 64k "$W/import.m4a"
cp "$W/import.m4a" "$FILES/File Provider Storage/RapatUjiE2E.m4a"

T=$(curl -fs -X POST "$B/v1/auth/dev" -H 'Content-Type: application/json' -d "{\"subject\":\"$SUBJECT\"}" | jq -r .token)
H=(-H "Authorization: Bearer $T")
MID=$(uuid)
curl -fs "${H[@]}" -X POST "$B/v1/meetings" -H 'Content-Type: application/json' \
  -d "{\"id\":\"$MID\",\"title\":\"Rapat dari server\",\"timezone\":\"Asia/Jakarta\",\"started_at\":\"2026-11-05T02:00:00Z\",\"language\":\"id\",\"participants\":[\"Budi\",\"Sari\"]}" >/dev/null
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=330:duration=30" -ac 1 -ar 44100 -c:a aac -b:a 64k "$W/a.m4a"
SIZE=$(wc -c <"$W/a.m4a" | tr -d ' ')
SHA=$(shasum -a 256 "$W/a.m4a" | cut -d' ' -f1)
UP=$(curl -fs "${H[@]}" -X POST "$B/v1/meetings/$MID/uploads" -H 'Content-Type: application/json' \
  -d "{\"size_bytes\":$SIZE,\"sha256\":\"$SHA\",\"content_type\":\"audio/mp4\",\"duration_ms\":30000,\"transcription\":{\"idempotency_key\":\"$(uuid)\",\"language\":\"id\"}}" | jq -r .upload.id)
curl -fs "${H[@]}" -X PUT "$B/v1/uploads/$UP" -H 'Content-Type: application/octet-stream' -H 'Upload-Offset: 0' --data-binary "@$W/a.m4a" >/dev/null
for _ in $(seq 1 100); do
  [ "$(curl -fs "${H[@]}" "$B/v1/meetings/$MID" | jq -r .meeting.pipeline_state)" = notes_ready ] && break
  sleep 0.3
done
curl -fs "${H[@]}" -X POST "$B/v1/meetings/$MID/documents/mom/generate" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuid)" -d '{"ai_notes_revision_id":null,"language":null}' >/dev/null
for _ in $(seq 1 100); do
  s=$(curl -fs "${H[@]}" "$B/v1/meetings/$MID" | jq -r .meeting.jobs.generate_mom.status)
  [ "$s" = succeeded ] && { echo "seeded $SUBJECT meeting $MID"; exit 0; }
  sleep 0.3
done
echo "MoM generation did not finish" >&2
exit 1
