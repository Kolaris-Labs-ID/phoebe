# Phoebe HTTP API v1

Contract between the iOS app and the Go backend. Backend design and invariants: `docs/backend.md`. Schema: `docs/schema.sql`.

## 1 Conventions

- Base path `/v1`. JSON request/response, `Content-Type: application/json; charset=utf-8`, UTF-8, snake_case keys.
- Auth: `Authorization: Bearer <token>` on every endpoint except `GET /healthz`, `GET /v1/config`, `POST /v1/auth/apple`, `POST /v1/auth/dev`, `GET /v1/files/...` (signed).
- IDs: lowercase UUID strings. Meeting IDs may be client-generated (UUIDv4).
- Timestamps: RFC3339, UTC, millisecond precision, always with fraction: `"2026-11-06T02:00:00.000Z"`. Inputs accept any RFC3339 offset; server normalizes to UTC. iOS: `Date.ISO8601FormatStyle(includingFractionalSeconds: true)` (Sendable).
- Dates without time (action item deadline): `"YYYY-MM-DD"`.
- Durations and offsets: integer milliseconds, fields end in `_ms`. Quota: integer seconds, fields end in `_seconds`. Sizes: bytes, fields end in `_bytes`.
- Nullable fields are always present with `null`; never omitted. This holds for responses and for document/transcript `content` in requests (a missing nullable content key → `422 invalid_content`). Clients must encode `nil` as `null` explicitly.
- `PATCH` semantics: omitted field = unchanged; explicit `null` = clear (nullable fields only).
- Unknown request fields are ignored. JSON body limit 2 MiB (`413 payload_too_large`); chunk uploads have their own limit.
- Correlation: client may send `X-Request-ID: <uuid>`; server generates one otherwise and echoes `X-Request-ID` on every response.
- Ownership: any resource not owned by the caller, or tombstoned, returns `404 not_found` (never 403), so existence is not leaked.
- Languages: user/output language `id|en`; transcription language `auto|id|en`.

### 1.1 Error envelope

Every non-2xx response:

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "Document was changed by another device.",
    "details": { "current_revision_number": 7, "current_revision_id": "…" },
    "request_id": "8f0c…"
  }
}
```

`code` is the stable machine value; `message` is English, for logs/debug only — iOS localizes by `code`. `details` is an object (possibly `{}`), never contains meeting content.

`422 validation_failed` details: `{"fields": [{"field": "title", "reason": "too_long"}]}`. Reasons: `required`, `too_long`, `too_short`, `invalid`, `not_found`, `duplicate`, `out_of_range`.

### 1.2 Error codes

| HTTP | code | When |
|---|---|---|
| 400 | `invalid_json` | Body not parseable / wrong types |
| 400 | `missing_idempotency_key` | `Idempotency-Key` header absent or not a UUID |
| 401 | `unauthenticated` | Missing, unknown, expired, or revoked token; account deleted |
| 402 | `quota_exceeded` | Not enough remaining quota. details `{required_seconds, remaining_seconds}` |
| 402 | `entitlement_inactive` | Entitlement inactive or outside its period. details `{period_end}` |
| 403 | `invalid_signature` | Signed file URL signature wrong |
| 404 | `not_found` | Resource absent, not owned, or tombstoned; also unknown route |
| 409 | `revision_conflict` | `expected_revision` ≠ current. details `{current_revision_number, current_revision_id}` |
| 409 | `job_in_progress` | Same-type job queued/running for this meeting. details `{job_id}` |
| 409 | `already_transcribed` | Meeting already has a transcript |
| 409 | `audio_exists` | Meeting already has verified audio |
| 409 | `audio_not_ready` | No verified audio yet |
| 409 | `transcript_not_ready` | Operation needs a transcript |
| 409 | `ai_notes_not_ready` | MoM generation needs an AI notes revision |
| 409 | `upload_offset_mismatch` | `Upload-Offset` ≠ received bytes. details `{received_bytes}` |
| 409 | `upload_busy` | Another chunk write or verification holds the session's writer lease |
| 409 | `upload_not_open` | Session completed/failed/canceled. details `{status}` |
| 409 | `upload_incomplete` | Complete called before all bytes received. details `{received_bytes, size_bytes}` |
| 409 | `name_taken` | Folder/tag name exists (case-insensitive) |
| 409 | `meeting_id_conflict` | Client-supplied meeting id used by another owner, or the meeting was deleted (permanent, survives purge) |
| 409 | `id_conflict` | Client-supplied folder/tag id used by another owner |
| 409 | `metadata_conflict` | `expected_metadata_revision` ≠ current. details `{current_metadata_revision}` |
| 410 | `upload_expired` | Upload session past `expires_at` |
| 410 | `audio_unavailable` | Audio deleted by user or expired by retention |
| 410 | `url_expired` | Signed file URL past `exp` |
| 413 | `payload_too_large` | JSON body > 2 MiB |
| 413 | `chunk_too_large` | Chunk > `upload_chunk_max_bytes` |
| 413 | `upload_too_large` | Declared `size_bytes` > `max_upload_bytes`, or chunk would exceed declared size |
| 422 | `validation_failed` | Field validation; see details |
| 422 | `invalid_content` | Document/transcript content invalid. details `{path, reason}` (JSON-pointer path) |
| 422 | `idempotency_key_reused` | Same `Idempotency-Key` or `mutation_id` used with a different request body |
| 422 | `checksum_mismatch` | SHA-256 of received bytes ≠ declared. Session becomes `failed` |
| 422 | `audio_unsupported` | Content is not M4A(AAC)/MP3/WAV, or contains video |
| 422 | `audio_corrupt` | Container unreadable / no duration |
| 422 | `audio_empty` | Duration < 1000 ms |
| 422 | `audio_too_long` | Duration > max. details `{duration_ms, max_duration_ms}` |
| 429 | `rate_limited` | AI generation limit hit. `Retry-After` header + details `{retry_after_seconds}` |
| 500 | `internal` | Bug; request_id for support |
| 503 | `unavailable` | Database unavailable |

### 1.3 Pagination

Cursor-based. Query `limit` (default 50, max 200) and `cursor` (opaque string from previous response). Response:

```json
{ "items": [ … ], "next_cursor": "eyJzIjoi…" }
```

`next_cursor` is `null` on the last page. Cursors are stable under inserts (keyset). Invalid cursor → `422 validation_failed` field `cursor`.

### 1.4 Idempotency

Job-creating endpoints (`POST …/transcription`, `POST …/documents/{kind}/generate`) require header `Idempotency-Key: <uuid>`. iOS generates the key once per user intent, persists it locally, and reuses it on every retry of that intent.

- First request: creates job → `202`.
- Same key, same request (same route, meeting, body): returns the same job in its current state → `200`. No new job, reservation, or revision.
- Same key, different request → `422 idempotency_key_reused`.
- Key lookup happens after auth + ownership and before all other validation. A request rejected with 4xx stores nothing; the key may be retried.

Document/transcript writes are idempotent via `mutation_id` (UUID in body, generated once per distinct body): replaying a write whose `mutation_id` already produced a revision with the same body returns that revision with `200`, even if the document has since advanced. The same `mutation_id` with a different body → `422 idempotency_key_reused` (server stores a fingerprint of the body). A client retrying after an unknown outcome (timeout, transport error, 5xx) must resend the identical body with the same `mutation_id`.

## 2 Shared objects

### User
```json
{ "id": "uuid", "display_name": "Ari", "language": "id", "timezone": "Asia/Jakarta", "created_at": "…" }
```

### Folder / Tag
```json
{ "id": "uuid", "name": "Klien A", "meeting_count": 4, "created_at": "…", "updated_at": "…" }
```
Name: trimmed, 1–60 chars, unique per user case-insensitive.

### AudioAsset
```json
{
  "id": "uuid", "content_type": "audio/mp4", "size_bytes": 28311552, "duration_ms": 3600512,
  "sha256": "hex64", "available": true, "deleted_reason": null,
  "retain_until": null, "created_at": "…"
}
```
`deleted_reason`: `null|user|expired`. `available=false` → playback disabled; transcript and documents stay. `retain_until` is `null` until the original transcript exists; retention (`audio_retention_days`) counts from transcription success, so untranscribed audio (waiting for quota, failed transcription) is never expired.

### UploadSession
```json
{
  "id": "uuid", "meeting_id": "uuid", "status": "open",
  "content_type": "audio/mp4", "size_bytes": 28311552, "received_bytes": 8388608,
  "sha256": "hex64", "duration_ms": 3600000, "chunk_max_bytes": 33554432,
  "failure_reason": null, "auto_transcription": true, "expires_at": "…", "created_at": "…"
}
```
`status`: `open|verifying|completed|failed|expired|canceled` (`verifying` = all bytes received, server checking checksum and content). `failure_reason`: `null|checksum_mismatch|audio_unsupported|audio_corrupt|audio_empty|audio_too_long`. `auto_transcription`: bool, whether transcription starts automatically on verification (create body `transcription`).

### Job
```json
{
  "id": "uuid", "meeting_id": "uuid", "type": "transcribe", "trigger": "user",
  "status": "running", "attempts": 1, "max_attempts": 3,
  "error_category": null,
  "result": null,
  "created_at": "…", "started_at": "…", "finished_at": null
}
```
- `type`: `transcribe|generate_notes|generate_mom`. Purge jobs are never exposed.
- `trigger`: `user|system` (automatic notes after transcript = `system`).
- `status`: `queued|running|succeeded|failed|canceled`. `queued` with `attempts>0` = waiting to retry.
- PRD MoM states: `queued`=requested, `running`=generating, `succeeded`=draft ready.
- `error_category` (last error; set while retrying and on failure): `provider_unavailable|provider_timeout|provider_rate_limited|provider_rejected|invalid_output|audio_unavailable|audio_unreadable|no_speech|timeout|internal`.
- `result` on success: transcribe `{"transcript_revision_id": "uuid"}`; generate_* `{"document_revision_id": "uuid", "dropped_items": 0}` (`dropped_items` = AI decisions/action items removed for lacking a valid source).

### SourceRef
```json
{ "type": "transcript", "segment_id": "s42", "start_ms": 734000, "end_ms": 741500 }
{ "type": "user_notes", "note_id": "n3" }
```
Transcript refs resolve against the revision's `source.transcript_revision_id`; `start_ms/end_ms` are always overwritten by the server from that revision. User-notes refs resolve against `source.manual_notes_revision_id`. Render transcript refs as `[mm:ss]` (`[h:mm:ss]` ≥ 1 h); user-notes refs as "Dari catatan pengguna" / "From user notes".

### Transcript revision
```json
{
  "id": "uuid", "revision_number": 1, "kind": "original", "language": "id",
  "created_at": "…",
  "segments": [
    { "id": "s1", "start_ms": 0, "end_ms": 4200, "text": "Selamat pagi semua." }
  ]
}
```
`kind`: `original|correction`. Segment ids `s1…sN` assigned in order on the original and stable across corrections; corrections change `text` only. Segments are sorted by `start_ms`, `0 ≤ start_ms ≤ end_ms ≤ audio duration`.

### Document summary
```json
{
  "id": "uuid", "kind": "mom", "current_revision_number": 3, "current_revision_id": "uuid",
  "review_status": "draft", "is_stale": true, "stale_reasons": ["transcript"], "updated_at": "…"
}
```
- `kind`: `manual_notes|ai_notes|mom`.
- `review_status`: `draft|reviewed` — `reviewed` iff the current revision has `reviewed_at`.
- `is_stale` ("Based on earlier version"): current revision's pinned sources differ from current sources. `stale_reasons` ⊆ `transcript` (pinned transcript revision ≠ latest), `manual_notes` (pinned manual notes revision ≠ latest, null-safe), `ai_notes` (mom only: pinned AI notes revision ≠ current AI notes revision). Revisions with all-null sources (manual, or manually created MoM) are never stale. `manual_notes` documents are never stale.

### Document revision
```json
{
  "id": "uuid", "document_id": "uuid", "kind": "mom", "revision_number": 3,
  "origin": "user_edit", "restored_from_revision_number": null,
  "reviewed_at": null, "created_at": "…",
  "source": {
    "transcript_revision_id": "uuid", "transcript_revision_number": 1,
    "manual_notes_revision_id": "uuid", "manual_notes_revision_number": 2,
    "ai_notes_revision_id": "uuid", "ai_notes_revision_number": 1
  },
  "content": { … }
}
```
- `origin`: `ai_generated|user_edit|restore`.
- `source.*` null when not applicable. `ai_generated` revisions pin the sources used; `user_edit` copies the sources of the PUT's `base_revision` (the revision the content was derived from; §4.7); `restore` copies the restored revision's sources.
- Revision listings return the same object without `content`.

## 3 Document content schemas (`schema_version` 1)

Common rules (server-validated on every write; violations → `422 invalid_content`):
- Item/section `id`: string 1–64 chars `[A-Za-z0-9_-]`, unique within the document. Server-generated ids: `sec_N`, `it_N`. Client-created ids: `h_<uuid>`.
- `text`, `description`, `title`, `heading`: 1–10 000 chars after trim; `heading`/`title` ≤ 200. Empty strings are invalid everywhere (nullable fields use `null`). Clients keep empty in-progress items only in their local draft and strip them when serializing for `PUT`: items whose `text`/`description` is empty, topics with empty `title` and no points, manual-notes items with empty `text`. A section with zero items is valid.
- Nullable keys (`pic`, `deadline`, `deadline_phrase`, `at_ms`, `meeting_info.started_at`, action-items `style`) must be present; `null` when unset.
- `origin`: `ai|human`. `edited`: bool, client sets `true` when the user modifies an `ai` item. UI indicator must be text/icon, not color only: `ai`+`!edited` = "AI", `ai`+`edited` = "AI, diedit", `human` = "Ditambahkan pengguna".
- `sources`: array of SourceRef, ≤ 50. User-created items may have `[]`. A transcript ref requires the revision's pinned transcript; a user-notes ref requires the pinned manual notes and an existing `note_id` there.
- Content ≤ 1 MiB serialized; ≤ 50 sections; ≤ 500 items per section; ≤ 100 points per topic.

### 3.1 Manual notes
```json
{
  "schema_version": 1,
  "items": [
    { "id": "n1", "text": "Bahas budget Q1", "at_ms": 125000 },
    { "id": "n2", "text": "Follow up vendor", "at_ms": null }
  ]
}
```
`at_ms`: active-audio offset when typed during recording, else `null`. Item text ≤ 2000 chars, ≤ 1000 items. iOS keeps an item's id stable across edits (refs bind to ids). Manual notes are hints for AI, never proof of agreement.

### 3.2 AI notes
```json
{
  "schema_version": 1,
  "language": "id",
  "sections": [
    {
      "id": "sec_1", "heading": "Budget Q1", "style": "bullet",
      "items": [
        { "id": "it_1", "text": "Budget marketing naik 10%.", "origin": "ai", "edited": false,
          "sources": [ { "type": "transcript", "segment_id": "s12", "start_ms": 61000, "end_ms": 66000 } ] }
      ]
    }
  ]
}
```
`style`: `bullet|numbered|paragraph`. Sections may be added, removed, reordered, renamed.

### 3.3 MoM
```json
{
  "schema_version": 1,
  "language": "id",
  "meeting_info": {
    "title": "Weekly sync",
    "started_at": "2026-11-06T02:00:00.000Z",
    "timezone": "Asia/Jakarta",
    "participants": ["Budi", "Sari"],
    "agenda": [ TextItem ]
  },
  "sections": [
    { "id": "summary",      "type": "summary",      "heading": "Ringkasan",     "style": "paragraph", "items": [ TextItem ] },
    { "id": "discussion",   "type": "discussion",   "heading": "Pembahasan",    "style": "bullet",    "items": [ TopicItem ] },
    { "id": "decisions",    "type": "decisions",    "heading": "Keputusan",     "style": "numbered",  "items": [ TextItem ] },
    { "id": "action_items", "type": "action_items", "heading": "Action items",  "style": null,        "items": [ ActionItem ] },
    { "id": "open_issues",  "type": "open_issues",  "heading": "Hal terbuka",   "style": "bullet",    "items": [ TextItem ] },
    { "id": "follow_ups",   "type": "follow_ups",   "heading": "Tindak lanjut", "style": "bullet",    "items": [ TextItem ] },
    { "id": "h_…",          "type": "custom",       "heading": "Catatan lain",  "style": "bullet",    "items": [ TextItem ] }
  ]
}
```

```json
TextItem   { "id": "it_3", "text": "…", "origin": "ai", "edited": false, "sources": [SourceRef] }
TopicItem  { "id": "it_4", "title": "Rencana rilis", "origin": "ai", "edited": false, "sources": [SourceRef],
             "points": [ TextItem ] }
ActionItem { "id": "it_9", "description": "Siapkan laporan vendor",
             "pic": "Budi", "deadline": "2026-11-13", "deadline_phrase": "minggu depan",
             "origin": "ai", "edited": false, "sources": [SourceRef] }
```

Rules:
- `meeting_info` is a snapshot taken at generation from meeting metadata (title, started_at, timezone, participants); the user edits it inside the MoM independently of the meeting. `started_at` nullable. Participants: strings ≤ 100 chars, ≤ 100 entries; never AI-generated.
- Section `type`: `summary|discussion|decisions|action_items|open_issues|follow_ups|custom`. Each non-custom type at most once; any section may be deleted; `custom` unlimited. `style`: `bullet|numbered|paragraph` for all types except `action_items` where it is `null`.
- **Undetermined semantics**: `pic: null` = PIC not stated → render "Belum ditentukan"/"Not determined". `deadline: null` and `deadline_phrase: null` → "Belum ditentukan". `deadline: null`, `deadline_phrase` set → render the phrase (relative deadline not normalized). `deadline` set → render `2026-11-13 (minggu depan)`, or just the date if phrase null. Never send `""`; use `null`.
- AI-originated `decisions` and `action_items` items always carry ≥ 1 source (server drops any that do not). User-added (`human`) items need none.
- Default headings by `language`: id — Agenda, Ringkasan, Pembahasan, Keputusan, Action items, Hal terbuka, Tindak lanjut; en — Agenda, Summary, Discussion, Decisions, Action items, Open issues, Follow-ups.
- Empty MoM template (manual creation when AI is unavailable): `meeting_info` from meeting, the six standard sections with empty `items`.

## 4 Endpoints

### 4.0 Health and config

`GET /healthz` → `200 {"status":"ok"}` (DB ping) or `503`.

`GET /v1/config` (no auth) → `200`
```json
{
  "max_audio_duration_ms": 7200000, "max_upload_bytes": 262144000, "upload_chunk_max_bytes": 33554432,
  "supported_content_types": ["audio/mp4", "audio/mpeg", "audio/wav"],
  "audio_retention_days": 30, "languages": ["id", "en"]
}
```
iOS validates imports against these before upload and shows retention before upload.

### 4.1 Auth and account

**POST /v1/auth/apple** (no auth)
```json
{ "identity_token": "eyJ…", "nonce": "raw-nonce-string", "display_name": "Ari Rudiana", "authorization_code": "c1a…|null" }
```
`nonce` = raw nonce; iOS sends `sha256hex(raw)` as the Apple request nonce. `display_name` optional (Apple gives the name only on first authorization; iOS keeps it until a 2xx); used only when creating the user or when the stored name is empty. `authorization_code` (optional, from `ASAuthorizationAppleIDCredential.authorizationCode`): exchanged server-side for a refresh token, stored encrypted, used only to revoke Apple tokens when the account is deleted. → `200`
```json
{ "token": "phb_…", "expires_at": "…", "created": true, "user": User }
```
Errors: `401 unauthenticated` (bad signature, iss, aud, exp, nonce), `422 validation_failed`.

**POST /v1/auth/dev** (no auth; route exists only when `DEV_LOGIN_ENABLED=true`, else `404`)
```json
{ "subject": "alice" }
```
`subject`: 1–64 chars `[a-z0-9._-]`. Same response as Apple.

**POST /v1/auth/logout** → `204`. Revokes the calling session.

**GET /v1/me** → `200 {"user": User}`

**PATCH /v1/me**
```json
{ "display_name": "Ari", "language": "en", "timezone": "Asia/Makassar" }
```
`timezone` must be a valid IANA name. → `200 {"user": User}`

**DELETE /v1/me** → `202 {"purge_scheduled_at": "…"}`. Atomically: tombstones user and all meetings, revokes all sessions, cancels jobs and uploads, releases reserved quota, schedules purge (which also revokes the Apple tokens). Subsequent calls with the old token → `401`. Signing in again with the same Apple ID creates a new, empty account without a new trial (entitlement `quota_seconds: 0`; a keyed hash of the Apple subject is retained to enforce this).

### 4.2 Meetings

Meeting object:
```json
{
  "id": "uuid", "source": "recording", "title": "Weekly sync", "started_at": "…", "timezone": "Asia/Jakarta",
  "language": "auto", "participants": ["Budi", "Sari"], "metadata_revision": 3, "folder_id": null, "tag_ids": ["uuid"],
  "pipeline_state": "notes_ready", "created_at": "…", "updated_at": "…",
  "audio": AudioAsset,
  "upload": null,
  "transcript": { "current_revision_id": "uuid", "current_revision_number": 1 },
  "documents": { "manual_notes": DocumentSummary, "ai_notes": DocumentSummary, "mom": null },
  "jobs": { "transcribe": Job, "generate_notes": Job, "generate_mom": null }
}
```
- `audio`, `upload` (latest `open` session), `transcript`, each `documents.*` and `jobs.*` (latest job of that type) are `null` when absent.
- `pipeline_state`: `awaiting_audio|uploading|uploaded|transcribing|transcription_failed|generating_notes|notes_failed|notes_ready`. PRD "transcript ready" = `transcript != null`. MoM progress = `jobs.generate_mom.status`.
- `source`: `recording|import`, set on create, immutable.
- `updated_at` changes on every change visible in this object: metadata, folder/tags, `pipeline_state`, any job status change (incl. retries), upload session changes, audio verify/delete/expiry, new transcript or document revision, review set/clear (backend.md §3.5). Clients refetch a meeting only when `updated_at` changed.

Meeting list item:
```json
{
  "id": "uuid", "source": "recording", "title": "…", "started_at": "…", "timezone": "…", "folder_id": null, "tag_ids": [],
  "pipeline_state": "notes_ready", "duration_ms": 3600512, "audio_available": true,
  "mom_review_status": null, "active_job_types": [], "failed_stage": null, "transcription_error_category": null,
  "created_at": "…", "updated_at": "…"
}
```
- `duration_ms` null without audio; `mom_review_status` `null|draft|reviewed`.
- `active_job_types`: subset of `transcribe|generate_notes|generate_mom` with a `queued|running` job.
- `failed_stage`: `null|transcription|notes|mom` — first of: `pipeline_state=transcription_failed`, `pipeline_state=notes_failed`, latest `generate_mom` job `failed`.
- `transcription_error_category`: latest `transcribe` job's `error_category` when it `failed` (e.g. `no_speech`), else null.

Validation (create/patch): `title` ≤ 200 chars (may be `""`; iOS shows a placeholder); `timezone` valid IANA; `language` `auto|id|en`; `participants` ≤ 100 strings, each trimmed 1–100 chars (clients trim and drop empty entries before sending); `folder_id` owned folder; `tag_ids` ≤ 20 owned tags, deduplicated. Unknown/unowned `folder_id`/`tag_ids` → `422 validation_failed` field `folder_id`/`tag_ids` reason `not_found`.

**POST /v1/meetings**
```json
{
  "id": "uuid-or-null", "source": "recording", "title": "", "started_at": "…|null", "timezone": "Asia/Jakarta",
  "language": "auto", "participants": [], "folder_id": null, "tag_ids": []
}
```
`id` optional client UUID for offline-created meetings. `source` `recording|import` (default `recording`). `started_at` null when unknown (e.g., imported file without a confirmed date). → `201 {"meeting": Meeting}`. If `id` already exists for this owner → `200` with the existing meeting (body ignored). Exists for another owner, or this meeting was ever deleted (also after purge) → `409 meeting_id_conflict`; a deleted meeting id can never be recreated.

**GET /v1/meetings** → `200 {"items": [MeetingListItem], "next_cursor": …}`

Query:
- `q` (1–200 chars): case-insensitive substring match on title, tag names, latest transcript revision text, and latest revision text of manual notes, AI notes, MoM.
- `folder_id`: UUID, or `none` for unfiled.
- `tag_id`: UUID (single).
- `limit`, `cursor`.

Filters AND together. Order: `coalesce(started_at, created_at)` desc, then `id` desc. Only caller's non-tombstoned meetings.

**GET /v1/meetings/{id}** → `200 {"meeting": Meeting}`

**PATCH /v1/meetings/{id}**
```json
{ "expected_metadata_revision": 3, "title": "…", "started_at": "…|null", "timezone": "…", "language": "en",
  "participants": ["…"], "folder_id": "uuid|null", "tag_ids": ["uuid"] }
```
- Metadata fields (`title, started_at, timezone, language, participants`): if `expected_metadata_revision` is present and ≠ current → `409 metadata_conflict {current_metadata_revision}`, nothing applied. A change to any of them increments `metadata_revision`. iOS always sends `expected_metadata_revision` with metadata changes (the value its edit was based on).
- `folder_id`, `tag_ids`: last-write-wins, never revision-checked; `tag_ids` replaces the set.
- `language` = transcription language; may be changed any time, used by the next transcription start.
- Changing metadata never alters existing MoM `meeting_info`. → `200 {"meeting": Meeting}`

**DELETE /v1/meetings/{id}** → `202 {"purge_scheduled_at": "…"}`. Tombstones the meeting, cancels its queued/running jobs and live upload sessions, releases `reserved` quota (consumed stays consumed), schedules purge of all rows and files. The id stays reserved forever (`POST /v1/meetings` with it → `409 meeting_id_conflict`). Repeat on a tombstoned meeting → `404` (iOS treats `404` on delete as success).

### 4.3 Folders and tags

Identical shapes for `/v1/folders` and `/v1/tags`.

- `GET /v1/folders` → `200 {"items": [Folder]}` (all, sorted by name; no pagination)
- `POST /v1/folders` `{"id": "uuid-or-null", "name": "Klien A"}` → `201 {"folder": Folder}`; `409 name_taken`. `id` optional client UUID (iOS always sends one, so folders created offline can be referenced before the create succeeds): same owner + same id → `200` with the existing folder (body ignored; replay after a lost response); another owner → `409 id_conflict`.
- `PATCH /v1/folders/{id}` `{"name": "…"}` → `200 {"folder": Folder}`; `409 name_taken`
- `DELETE /v1/folders/{id}` → `204`. Meetings in it become unfiled.
- `GET /v1/tags`, `POST /v1/tags` (same optional `id` semantics) → `{"tag": Tag}`, `PATCH /v1/tags/{id}`, `DELETE /v1/tags/{id}` (removes tag from all meetings).

Folders are one level; there is no parent field. Move a meeting via `PATCH /v1/meetings/{id}` `folder_id`.

### 4.4 Uploads

Flow: create session → PUT chunks (resume via GET) → last chunk verifies and (optionally) starts transcription → explicit complete / start transcription calls are idempotent confirmations.

**POST /v1/meetings/{id}/uploads**
```json
{ "size_bytes": 28311552, "sha256": "hex64-lowercase", "content_type": "audio/mp4", "duration_ms": 3600000,
  "transcription": { "idempotency_key": "uuid", "language": "auto" } }
```
- `content_type`: `audio/mp4` (aliases `audio/m4a`, `audio/x-m4a`), `audio/mpeg`, `audio/wav` (aliases `audio/x-wav`, `audio/wave`); stored normalized. Declared type is a hint; the server detects the real type at complete.
- `duration_ms`: client-measured; used only for early rejection (`audio_too_long`). Server-computed duration is authoritative.
- `transcription` optional (`null`/absent = no auto-start). When set, verification of the last byte starts transcription server-side in the same transaction with this key, exactly as `POST …/transcription` with header `Idempotency-Key: <idempotency_key>` and body `{"language": "<language>"}` would. Lets processing start while the app is suspended or closed (PRD §7).
- If a live (`open`/`verifying`) session exists with the same `sha256` and `size_bytes` → `200` with it (resume). With different values → `409 upload_busy` while it is being written or verified, else old session `canceled` and a new one created.
- → `201 {"upload": UploadSession}`. Meeting `pipeline_state` → `uploading`.
- Errors: `409 audio_exists`, `409 upload_busy`, `413 upload_too_large`, `422 audio_too_long`, `422 validation_failed`.

**GET /v1/uploads/{upload_id}** → `200 {"upload": UploadSession}`. Use `received_bytes` as resume offset.

**PUT /v1/uploads/{upload_id}**
Headers: `Content-Type: application/octet-stream`, `Upload-Offset: <int>`, `Content-Length: <int>` (required). Body: raw bytes `[offset, offset+len)`.
- `Upload-Offset` must equal `received_bytes` → else `409 upload_offset_mismatch` (details give the correct offset).
- If the connection drops mid-body, bytes already persisted count; client re-reads offset via GET and continues.
- → `200 {"upload": UploadSession, "audio": null, "job": null}`.
- The chunk that makes `received_bytes == size_bytes` runs complete inline: the response is exactly the complete response below (`200 {"upload", "audio", "job"}`) or its errors (`422 checksum_mismatch|audio_*`).
- Errors: `409 upload_busy` (retry after 1–5 s), `409 upload_not_open`, `410 upload_expired`, `413 chunk_too_large`, `413 upload_too_large`.
- iOS background `URLSession` upload tasks: one chunk per task, chunk = `min(upload_chunk_max_bytes, remaining)`.

**POST /v1/uploads/{upload_id}/complete** (empty body) → `200`
```json
{ "upload": UploadSession, "audio": AudioAsset, "job": Job }
```
Server verifies `received_bytes == size_bytes`, SHA-256, real content type, duration (`> max_audio_duration_ms` rejected, no tolerance), limits; then creates the audio asset, sets `pipeline_state` → `uploaded`, and, if the session has `auto_transcription`, starts transcription (`job` = the transcribe job; `null` if not requested or not startable, e.g. quota — the explicit `POST …/transcription` returns the reason). Idempotent: completing a `completed` session returns the same result (`job` = latest transcribe job). iOS keeps its local audio at least until a transcript exists, and deletes it then only per user preference.
Errors: `409 upload_incomplete`, `409 upload_busy` (verification in progress; retry), `409 upload_not_open` (failed/canceled), `410 upload_expired`, `422 checksum_mismatch|audio_unsupported|audio_corrupt|audio_empty|audio_too_long` (session → `failed`, meeting → `awaiting_audio`; create a new session).

### 4.5 Transcription and jobs

**POST /v1/meetings/{id}/transcription**
Header `Idempotency-Key`. Body (optional fields):
```json
{ "language": "auto" }
```
`language` defaults to meeting `language`. Reserves quota (`ceil(duration_ms/1000)` s) and enqueues `transcribe`. Allowed from `uploaded` or `transcription_failed`. → `202 {"job": Job}` (replay `200`). Meeting → `transcribing`. To confirm an auto-started transcription (§4.4), send the same key and the identical body `{"language": "<same value>"}` → `200` replay.
Errors: `400 missing_idempotency_key`, `409 audio_not_ready`, `410 audio_unavailable`, `409 already_transcribed`, `409 job_in_progress`, `402 quota_exceeded`, `402 entitlement_inactive`, `422 idempotency_key_reused`.

After the transcript is written, the server automatically enqueues `generate_notes` (`trigger: system`, not rate-limited). Failure categories `no_speech`, `audio_unreadable`, `audio_unavailable` are permanent; quota is released on any permanent failure.

**GET /v1/meetings/{id}/jobs** → `200 {"items": [Job]}` (newest first, max 50, no cursor).

**GET /v1/jobs/{job_id}** → `200 {"job": Job}`

Polling (no push in MVP): while any job of a visible meeting is `queued|running`, poll `GET /v1/meetings/{id}` (Library: first page of `GET /v1/meetings`, rows with non-empty `active_job_types`) with backoff 2 s ×1.5 up to 30 s, foreground only; resume on app foreground. Stop when no active jobs.

### 4.6 Transcript

**GET /v1/meetings/{id}/transcript** → `200 {"revision": TranscriptRevision}` (current = highest number). `409 transcript_not_ready` if none.

**GET /v1/meetings/{id}/transcript/revisions** → paginated, newest first, items without `segments`.

**GET /v1/meetings/{id}/transcript/revisions/{n}** → `200 {"revision": TranscriptRevision}`

**POST /v1/meetings/{id}/transcript/revisions** (correction)
```json
{ "expected_revision": 1, "mutation_id": "uuid", "edits": [ { "segment_id": "s12", "text": "Budget naik 10 persen." } ] }
```
Creates revision `expected_revision+1`, kind `correction`, copying all segments and replacing text of edited ones (text ≤ 2000 chars, may be empty; unknown/duplicate segment id → `422 invalid_content`; ≥ 1 edit). Does not regenerate notes/MoM; they become stale. No quota. → `201 {"revision": TranscriptRevision}`; replayed `mutation_id` → `200`. `409 revision_conflict`.

### 4.7 Documents

`{kind}` ∈ `manual_notes|ai_notes|mom`; anything else → `404`.

**GET /v1/meetings/{id}/documents/{kind}** → `200`
```json
{ "document": DocumentSummary, "revision": DocumentRevision }
```
`404 not_found` if the document has no revision yet.

**PUT /v1/meetings/{id}/documents/{kind}** (save edit; creates a revision)
```json
{ "expected_revision": 3, "base_revision": 3, "mutation_id": "uuid", "content": { … } }
```
- `expected_revision` = the current revision the client believes it is replacing (concurrency check); `0` creates the document (any kind; e.g., manual MoM when AI is unavailable). Mismatch → `409 revision_conflict`.
- `base_revision` (optional, default = `expected_revision`; `0..expected_revision`) = the revision the content was derived from (provenance). Sources are copied from it and refs validated against its pinned sources; `0` = no sources.
- New revision `origin: user_edit`, `reviewed_at: null` (status returns to Draft).
- → `201 {"document": DocumentSummary, "revision": DocumentRevision}`; replayed `mutation_id` with the same body → `200`; same `mutation_id`, different body → `422 idempotency_key_reused`.
- Errors: `409 revision_conflict`, `422 invalid_content`, `422 idempotency_key_reused`.

Conflict handling (iOS): on `409`, keep the local draft, fetch current, show "Changed on another device / by regenerate"; options "Keep mine" (re-PUT with `expected_revision` = current, `base_revision` = the revision the draft was derived from, new `mutation_id`; the result is correctly stale if its base is older) or "Use latest" (discard local). Never auto-merge.

**GET /v1/meetings/{id}/documents/{kind}/revisions** → paginated, newest first, DocumentRevision without `content`.

**GET /v1/meetings/{id}/documents/{kind}/revisions/{n}** → `200 {"revision": DocumentRevision}`

**POST /v1/meetings/{id}/documents/{kind}/restore**
```json
{ "revision_number": 2, "expected_revision": 5, "mutation_id": "uuid" }
```
New revision (`origin: restore`) with the content and sources of revision 2. → `201 {"document", "revision"}`; replay rules as PUT. `409 revision_conflict`, `404` unknown revision.

**PUT /v1/meetings/{id}/documents/{kind}/review**
```json
{ "status": "reviewed", "expected_revision": 5 }
```
Sets/clears `reviewed_at` on the current revision; no new revision; sends nothing to anyone. → `200 {"document": DocumentSummary}`. `409 revision_conflict` (you must review what you saw).

**POST /v1/meetings/{id}/documents/ai_notes/generate** (create or regenerate)
Header `Idempotency-Key`. Body:
```json
{ "language": null }
```
`language` `id|en|null` (null = user language). Pins current transcript revision and current server manual notes revision (iOS pushes pending manual-notes edits first). → `202 {"job": Job}`; meeting → `generating_notes`. Success creates a new AI notes revision that becomes current (prior revisions, including user edits, remain restorable).
Errors: `409 transcript_not_ready`, `409 job_in_progress`, `429 rate_limited`, `400 missing_idempotency_key`, `422 idempotency_key_reused`.

**POST /v1/meetings/{id}/documents/mom/generate** (create or regenerate)
Header `Idempotency-Key`. Body:
```json
{ "ai_notes_revision_id": "uuid-or-null", "language": null }
```
`ai_notes_revision_id` null = current AI notes revision; must belong to this meeting's AI notes. Pins it plus current transcript and manual notes revisions. → `202 {"job": Job}`. Success creates a new MoM revision (`origin: ai_generated`) that becomes current.
Errors: `409 ai_notes_not_ready`, `404` unknown revision, `409 job_in_progress`, `429 rate_limited`.
`manual_notes/generate` → `404`.

Rate limit: user-triggered `generate_notes` + `generate_mom` jobs per user per rolling 24 h ≤ `AI_USER_JOBS_PER_DAY`. Idempotent replays do not count.

### 4.8 Exports

**POST /v1/exports**
```json
{ "document_revision_id": "uuid", "format": "pdf" }
```
`format`: `md|whatsapp|docx|pdf`. Any kind may be exported. Rendered synchronously and deterministically from that immutable revision (no LLM). Draft label iff the revision's `reviewed_at` is null at export time. → `201`
```json
{
  "export": {
    "id": "uuid", "document_revision_id": "uuid", "kind": "mom", "format": "pdf",
    "filename": "weekly-sync-20261106-mom-draft.pdf", "content_type": "application/pdf",
    "size_bytes": 48211, "is_draft": true, "created_at": "…"
  },
  "download_url": "https://api…/v1/files/export/<id>?exp=1762400000&sig=…",
  "download_expires_at": "…",
  "text": null
}
```
`text`: full rendered text for `md` and `whatsapp` (for Copy text / share as text), else `null`. Content types: md `text/markdown; charset=utf-8`, whatsapp `text/plain; charset=utf-8` (`.txt`), docx `application/vnd.openxmlformats-officedocument.wordprocessingml.document`, pdf `application/pdf`.
Errors: `404` revision, `422 validation_failed`.

**GET /v1/exports/{export_id}** → `200 {"export", "download_url", "download_expires_at", "text": null}` (fresh URL).

### 4.9 Audio playback and signed files

**GET /v1/meetings/{id}/audio/url** → `200 {"url": "…/v1/files/audio/<asset_id>?exp=…&sig=…", "expires_at": "…"}`. `409 audio_not_ready`, `410 audio_unavailable`. iOS plays its local copy when present; otherwise downloads the whole file promptly (validity is checked at request start) and plays from cache.

**GET /v1/files/{kind}/{id}?exp=<unix>&sig=<b64url>** (no bearer auth) — `kind` ∈ `audio|export`. Supports `Range` (`206`). Headers: `Cache-Control: private, no-store`; exports `Content-Disposition: attachment; filename="…"`. Errors: `403 invalid_signature`, `410 url_expired`, `404` (deleted/tombstoned/purged). URL TTL `SIGNED_URL_TTL` (default 15 min).

### 4.10 Usage and entitlement

**GET /v1/usage** → `200`
```json
{
  "entitlement": { "plan": "trial", "status": "active", "period_start": "…", "period_end": "…", "quota_seconds": 7200 },
  "consumed_seconds": 3601, "reserved_seconds": 0, "remaining_seconds": 3599,
  "ai_generations": { "limit_per_day": 30, "used_last_24h": 2, "next_available_at": null }
}
```
`status`: `active|expired|inactive`. `consumed_seconds` = current period; `reserved_seconds` = all in-flight reservations; `remaining_seconds = max(0, quota − consumed − reserved)`. `next_available_at`: when `used_last_24h ≥ limit_per_day`, the instant the oldest counted generation leaves the 24 h window; else `null`. UI shows minutes (floor). Recording/local save is never blocked by quota; iOS shows "Transcription waits for quota" on `402 quota_exceeded` and "Plan ended" on `402 entitlement_inactive`.

### 4.11 Deletion

| Endpoint | Effect | Response |
|---|---|---|
| `DELETE /v1/meetings/{id}/audio` | Marks the audio deleted (playback and transcription start fail immediately), then deletes bytes; transcript, notes, MoM, exports stay; `deleted_reason: user`. Already deleted → `204`. | `204`; `409 job_in_progress` if transcribe queued/running; `409 audio_not_ready` if none |
| `DELETE /v1/meetings/{id}` | See 4.2 | `202` |
| `DELETE /v1/me` | See 4.1 | `202` |

### 4.12 Events

**POST /v1/events**
```json
{ "events": [ { "event_id": "uuid", "name": "recording_started", "occurred_at": "…", "meeting_id": "uuid|null" } ] }
```
- 1–100 events. `name` ∈ `onboarding_completed|recording_started|recording_interrupted|recording_saved|document_saved|mom_reviewed|export_completed|share_sheet_opened|quota_blocked`. Server-emitted names (`upload_verified|transcription_completed|notes_ready|mom_generated`) → `422 validation_failed`. No other fields; never meeting content.
- Deduplicated by `event_id` per user: resending a batch is safe. `meeting_id` not owned by the caller is stored as `null` (no error).
- → `202 {"accepted": 3}` (newly stored count).

## 5 Export rendering contract

All four formats render the same fields from one revision; decisions, numbers, PIC, deadlines must be textually identical across PDF/DOCX/MD.

Header (all formats): title (`meeting_info.title` for MoM; meeting title at export time for notes; empty → "Rapat tanpa judul"/"Untitled meeting"), date line `2026-11-06 09:00 WIB` (started_at in timezone, zone abbreviation from tzdata; null → "Tanggal belum ditentukan"/"Date not set"), participants line (MoM, if any), and Draft label "DRAFT — belum ditinjau"/"DRAFT — not reviewed" when draft. Label language = content `language` (manual notes: user language).

Body order (MoM): agenda (numbered) then sections in stored order. Empty sections are omitted, except `decisions` and `action_items`, rendered as "Tidak ada."/"None.". Refs: transcript → `[mm:ss]` appended to the item (multiple: `[01:02, 05:40]`); user notes → "(dari catatan pengguna)"/"(from user notes)". No URLs, storage keys, ids, or tokens in any output. Origin indicators are not exported.

- **md**: `#` title, `##` sections, `###` topics; `-` bullets, `1.` numbered; paragraph style = blank-line paragraphs; action items as table `| No | Tugas/Task | PIC | Deadline | Sumber/Source |`. Escape `\ * _ \` [ ] < > |` with backslash; escape leading `#`, `+`, `-`, `N.` at line starts; newlines in cells → space; trailing newline. Byte-identical for identical input.
- **whatsapp**: `*Title*`, date line, `_DRAFT…_`, then `*Ringkasan*` (summary), `*Keputusan*` (numbered), `*Action items*` (numbered; second line `   PIC: … · Deadline: …`). No other sections, no refs. Byte-identical.
- **docx**: Title + Heading1/Heading2/Heading3 styles, native numbering (`numbering.xml`; each numbered list restarts), action items as a 5-column table with header row; header line paragraph "DRAFT" when draft. Deterministic zip (fixed entry order, fixed timestamp = revision `created_at`).
- **pdf**: A4, 20 mm margins, embedded UTF-8 TTF (regular+bold), headings 18/14/12 pt, body 11 pt, wrapped text (no clipped lines across page breaks), action items as numbered blocks with PIC/Deadline/Source line, "DRAFT" in page header when draft, footer `n/N`, selectable text. Creation/modification dates = revision `created_at`.

Filename: `{slug}-{yyyymmdd}-{mom|notes|manual-notes}[-draft].{md|txt|docx|pdf}`; slug = ASCII lowercase `[a-z0-9-]` from title, ≤ 60 chars, fallback `meeting`; date from started_at in meeting timezone, else `undated`.

## 6 iOS sync sequence (offline-first)

1. Create meeting (and any new folder/tag) locally with client UUIDs; record/import; autosave manual notes locally.
2. When online: `POST /v1/meetings` (with `id`) → `PUT …/documents/manual_notes` (`expected_revision` from last server revision, 0 initially).
3. `POST …/uploads` (with `transcription` = the persisted transcribe intent key) → `PUT` chunks; the last one verifies and starts transcription → `POST …/complete` (confirm, idempotent).
4. `POST …/transcription` with the same `Idempotency-Key` and body → `200` replay, or `402` → keep meeting in "waiting for quota" / "plan ended" and retry when `/v1/usage` shows an active entitlement with enough quota.
5. Poll per 4.5; fetch transcript and AI notes on change. MoM only on user action.

Every step is safely retryable: meeting/folder/tag create by id, writes by `mutation_id` (same body), uploads by offset, jobs by idempotency key.
