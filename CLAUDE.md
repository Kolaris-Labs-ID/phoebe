# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

MVP implemented. Spec: `docs/Phoebe_PRD.md` (Indonesian, MVP v0.1, Kolaris Labs, owner Ari Rudiana); contracts `docs/api.md` (wins on conflicts), `docs/backend.md`, `docs/schema.sql`, `docs/ios.md`; UI `design-system/phoebe/`. What is verified end-to-end vs. pending a device: `docs/STATUS.md`.

Build/test commands:
- Backend (Go, Postgres via root `docker-compose.yml` on host port 55432): `backend/README.md` — `go vet ./...`, `go test ./... -count=1`, `backend/scripts/smoke.sh`, local run with fake providers + dev login.
- iOS (XcodeGen + SwiftUI): `ios/README.md` — `xcodegen generate`, unit tests (scheme `Phoebe`), screenshots and E2E UI tests against the local backend (scheme `PhoebeScreenshots`).

Phoebe = iOS app to record meetings → timestamped transcript → AI notes → editable MoM → export (PDF, DOCX, MD, WhatsApp text) via native share sheet. SaaS, quota by audio minutes.

## Scope rules

- P0 = MVP scope. P1 and "Di luar MVP" (PRD §1) are out of scope — don't build them.
- PRD distinguishes **decided** (Phoebe name, native Swift iOS, Go backend, SaaS, core feature set) from **proposed baseline to validate** (SwiftUI, PostgreSQL, object storage, DeepSeek Flash/Gemini, limits like 120 min / 250 MB, iOS 17 min, 30-day audio retention). Treat proposed values as configurable, not hard-coded.
- Pricing, plans, and final models are undecided. Don't invent them.

## Planned architecture (PRD §7)

- **iOS**: Swift/SwiftUI, AVFoundation, local persistence, background upload, Keychain tokens, Sign in with Apple. No model/provider names in the app — all AI config lives on backend.
- **Backend**: one modular Go app + separate worker process. PostgreSQL for metadata/documents and (MVP) the durable job queue. Private object storage for audio and exports.
- **AI**: provider adapters on backend. STT provider chosen separately from LLM. No hidden cross-provider fallback. Retry max 2 after initial attempt on API failure / invalid JSON.

### State machines
- Local recording: idle → recording → paused|interrupted → stopped → queued for upload
- Cloud processing: uploading → uploaded → transcribing → transcript ready → generating notes → notes ready
- MoM (separate job, user-triggered): requested → generating → draft ready
- Failure is per stage; a failed stage never makes the meeting inaccessible.

## Invariants (must hold in every change)

- **Recording never loses completed segments.** Record in recoverable segments; on interruption close the valid segment and show "Paused by interruption". Never auto-start the mic on relaunch; offer recovery. Local recording works offline and when quota is exhausted.
- **Artifacts are separate and versioned**: manual notes, AI notes, transcript, MoM. Every generated version records its source revision. Edits/regenerate/restore create new revisions; nothing overwrites. Source change → old outputs labeled "Based on earlier version", not auto-replaced. Updates use expected-revision checks.
- **No fabrication**: no invented PIC, deadline, participants, numbers, or decisions. Missing → empty + "belum ditentukan". Decisions/action items need a source segment reference or a "from user notes" label. Manual notes are hints, not proof of agreement.
- **Transcript is data**: instructions inside it must not alter system rules or trigger actions (prompt-injection guard).
- **Idempotency**: job-creation APIs take an idempotency key; retries/duplicate callbacks never create duplicate versions or usage charges. Processing starts only after upload verification (session ID + checksum). Workers write atomically, with timeout, lease, backoff.
- **Quota ledger**: stored in seconds, shown in minutes, no per-segment rounding. Reserve before transcription, consume once on success, release on permanent failure. Corrections, exports, system retries don't consume quota. Server computes duration; pauses not counted.
- **Exports are deterministic** from one revision snapshot — no LLM call. Include title, date, Draft label if unreviewed. Never leak storage URLs or tokens.
- **Review status**: Draft | Reviewed, user-set only; any content edit after Reviewed reverts to Draft.
- **Privacy/ownership**: every endpoint, file, search, and job verifies owner server-side; data keyed by internal user ID, not email. Meeting/transcript/notes content never goes into logs or analytics (log tokens, prompt version, model, provider, latency only). Delete meeting cascades and cancels jobs; workers check tombstones before writing. Delete audio keeps notes. Don't claim end-to-end encryption.

## Delivery stages (PRD §10)

A recording spike → B core pipeline → C MoM & sharing → D pilot → E paid launch. Acceptance scenarios in PRD §9 are the verification checklist.
