#!/usr/bin/env bash
# Live provider checks (real OpenAI STT + DeepSeek LLM calls; costs a few cents).
# Without PHOEBE_LIVE=1 and the keys, each test prints SKIPPED and passes.
#
#   OPENAI_API_KEY=sk-… DEEPSEEK_API_KEY=sk-… backend/scripts/live-check.sh
#
# Optional: PHOEBE_LIVE_STT_MODEL (default gpt-4o-mini-transcribe; also gpt-4o-transcribe, whisper-1),
# PHOEBE_LIVE_LLM_MODEL (default deepseek-flash), OPENAI_BASE_URL, DEEPSEEK_BASE_URL.
# Needs ffmpeg on PATH. No DATABASE_URL needed.
set -euo pipefail
cd "$(dirname "$0")/.."
[ -n "${OPENAI_API_KEY:-}" ] || echo "note: OPENAI_API_KEY not set, the STT live test will skip" >&2
[ -n "${DEEPSEEK_API_KEY:-}" ] || echo "note: DEEPSEEK_API_KEY not set, the LLM live test will skip" >&2
PHOEBE_LIVE=1 go test ./internal/ai/openai ./internal/ai/openaicompat -run '^TestLive' -count=1 -v
