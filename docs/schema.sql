-- Phoebe initial schema. Copied verbatim to backend/internal/db/migrations/0001_init.sql.
-- Requires PostgreSQL 16 (NULLS NOT DISTINCT, ON DELETE SET NULL (col)).
-- updated_at columns are set by application code.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    apple_sub               text,
    dev_sub                 text,
    display_name            text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 100),
    language                text NOT NULL DEFAULT 'id' CHECK (language IN ('id', 'en')),
    timezone                text NOT NULL DEFAULT 'Asia/Jakarta' CHECK (char_length(timezone) BETWEEN 1 AND 64),
    -- AES-256-GCM(TOKEN_ENC_KEY) of the Apple refresh token; used only to revoke at account purge.
    apple_refresh_token_enc bytea,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz,
    CHECK (apple_sub IS NOT NULL OR dev_sub IS NOT NULL)
);
-- A deleted (not yet purged) account does not block a fresh sign-in with the same identity.
CREATE UNIQUE INDEX users_apple_sub_key ON users (apple_sub) WHERE deleted_at IS NULL AND apple_sub IS NOT NULL;
CREATE UNIQUE INDEX users_dev_sub_key ON users (dev_sub) WHERE deleted_at IS NULL AND dev_sub IS NOT NULL;

-- HMAC-SHA256(SUBJECT_PEPPER, 'apple:'|'dev:' + sub). Survives account purge so that
-- delete + re-sign-in does not grant a second trial. Keyed hash only; no PII.
CREATE TABLE trial_grants (
    subject_hmac bytea PRIMARY KEY CHECK (octet_length(subject_hmac) = 32),
    granted_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE entitlements (
    user_id       uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    plan          text NOT NULL CHECK (plan IN ('trial', 'pilot')),
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    quota_seconds bigint NOT NULL CHECK (quota_seconds >= 0),
    period_start  timestamptz NOT NULL,
    period_end    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (period_end > period_start)
);

-- id may be client-supplied (offline-created folders); API generates otherwise.
CREATE TABLE folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60 AND name = btrim(name)),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX folders_user_name_key ON folders (user_id, lower(name));

CREATE TABLE tags (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60 AND name = btrim(name)),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX tags_user_name_key ON tags (user_id, lower(name));
CREATE INDEX tags_name_trgm_idx ON tags USING gin (name gin_trgm_ops);

-- id has no default: clients may supply it (offline-created meetings); API generates otherwise.
-- updated_at moves on every change visible in the meeting object (backend.md §3.5).
-- metadata_revision moves only when title/started_at/timezone/language/participants change.
CREATE TABLE meetings (
    id                uuid PRIMARY KEY,
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    folder_id         uuid,
    source            text NOT NULL DEFAULT 'recording' CHECK (source IN ('recording', 'import')),
    title             text NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    started_at        timestamptz,
    timezone          text NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 64),
    language          text NOT NULL DEFAULT 'auto' CHECK (language IN ('auto', 'id', 'en')),
    participants      text[] NOT NULL DEFAULT '{}' CHECK (cardinality(participants) <= 100),
    metadata_revision int NOT NULL DEFAULT 1 CHECK (metadata_revision >= 1),
    pipeline_state    text NOT NULL DEFAULT 'awaiting_audio' CHECK (pipeline_state IN (
                          'awaiting_audio', 'uploading', 'uploaded', 'transcribing', 'transcription_failed',
                          'generating_notes', 'notes_failed', 'notes_ready')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    sort_at           timestamptz GENERATED ALWAYS AS (COALESCE(started_at, created_at)) STORED,
    deleted_at        timestamptz,
    UNIQUE (id, user_id),
    -- composite FK keeps folder and meeting under the same owner
    FOREIGN KEY (folder_id, user_id) REFERENCES folders (id, user_id) ON DELETE SET NULL (folder_id)
);
CREATE INDEX meetings_user_sort_idx ON meetings (user_id, sort_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX meetings_folder_idx ON meetings (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX meetings_title_trgm_idx ON meetings USING gin (title gin_trgm_ops);

-- Permanent tombstone: written in the delete tx, kept after purge, so a stale outbox
-- can never recreate a deleted meeting id (POST /v1/meetings → 409 meeting_id_conflict).
CREATE TABLE deleted_meetings (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    deleted_at timestamptz NOT NULL
);

CREATE TABLE meeting_tags (
    meeting_id uuid NOT NULL,
    tag_id     uuid NOT NULL,
    user_id    uuid NOT NULL,
    PRIMARY KEY (meeting_id, tag_id),
    FOREIGN KEY (meeting_id, user_id) REFERENCES meetings (id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id, user_id) REFERENCES tags (id, user_id) ON DELETE CASCADE
);
CREATE INDEX meeting_tags_tag_idx ON meeting_tags (tag_id);

-- writer_lease_id fences chunk writers and the verifier (backend.md §7).
CREATE TABLE upload_sessions (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id                 uuid NOT NULL,
    user_id                    uuid NOT NULL,
    status                     text NOT NULL DEFAULT 'open' CHECK (status IN (
                                   'open', 'verifying', 'completed', 'failed', 'expired', 'canceled')),
    declared_content_type      text NOT NULL CHECK (declared_content_type IN ('audio/mp4', 'audio/mpeg', 'audio/wav')),
    size_bytes                 bigint NOT NULL CHECK (size_bytes > 0),
    sha256                     text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    declared_duration_ms       bigint NOT NULL CHECK (declared_duration_ms > 0),
    received_bytes             bigint NOT NULL DEFAULT 0,
    storage_key                text NOT NULL,
    writer_lease_id            uuid,
    writer_lease_until         timestamptz,
    -- optional auto-start of transcription when the last byte is verified (api.md §4.4)
    transcribe_idempotency_key uuid,
    transcribe_language        text CHECK (transcribe_language IN ('auto', 'id', 'en')),
    failure_reason             text CHECK (failure_reason IN (
                                   'checksum_mismatch', 'audio_unsupported', 'audio_corrupt', 'audio_empty', 'audio_too_long')),
    expires_at                 timestamptz NOT NULL,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    completed_at               timestamptz,
    FOREIGN KEY (meeting_id, user_id) REFERENCES meetings (id, user_id) ON DELETE CASCADE,
    CHECK (received_bytes >= 0 AND received_bytes <= size_bytes),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL)),
    CHECK ((status = 'failed') = (failure_reason IS NOT NULL)),
    CHECK ((writer_lease_id IS NULL) = (writer_lease_until IS NULL)),
    CHECK (status IN ('open', 'verifying') OR writer_lease_id IS NULL),
    CHECK (status <> 'verifying' OR received_bytes = size_bytes),
    CHECK ((transcribe_idempotency_key IS NULL) = (transcribe_language IS NULL))
);
CREATE UNIQUE INDEX upload_sessions_one_live_key ON upload_sessions (meeting_id) WHERE status IN ('open', 'verifying');
CREATE INDEX upload_sessions_expiry_idx ON upload_sessions (expires_at) WHERE status = 'open';

-- One audio asset per meeting. Row survives audio deletion (deleted_at) so duration and
-- playback state stay known. retain_until is NULL until the original transcript exists
-- (retention starts at transcription success). bytes_purged_at = storage object removed.
CREATE TABLE audio_assets (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id        uuid NOT NULL UNIQUE REFERENCES meetings (id) ON DELETE CASCADE,
    upload_session_id uuid NOT NULL UNIQUE REFERENCES upload_sessions (id) ON DELETE CASCADE,
    content_type      text NOT NULL CHECK (content_type IN ('audio/mp4', 'audio/mpeg', 'audio/wav')),
    size_bytes        bigint NOT NULL CHECK (size_bytes > 0),
    sha256            text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    duration_ms       bigint NOT NULL CHECK (duration_ms >= 1000),
    storage_key       text NOT NULL,
    retain_until      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,
    deleted_reason    text CHECK (deleted_reason IN ('user', 'expired')),
    bytes_purged_at   timestamptz,
    CHECK ((deleted_at IS NULL) = (deleted_reason IS NULL)),
    CHECK (bytes_purged_at IS NULL OR deleted_at IS NOT NULL)
);
CREATE INDEX audio_assets_retention_idx ON audio_assets (retain_until)
    WHERE deleted_at IS NULL AND retain_until IS NOT NULL;
CREATE INDEX audio_assets_bytes_pending_idx ON audio_assets (deleted_at)
    WHERE deleted_at IS NOT NULL AND bytes_purged_at IS NULL;

-- Durable queue. Purge jobs outlive their target rows, so they carry no FKs (target ids in payload).
CREATE TABLE jobs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type                text NOT NULL CHECK (type IN ('transcribe', 'generate_notes', 'generate_mom', 'purge_meeting', 'purge_account')),
    user_id             uuid REFERENCES users (id) ON DELETE CASCADE,
    meeting_id          uuid REFERENCES meetings (id) ON DELETE CASCADE,
    trigger             text NOT NULL CHECK (trigger IN ('user', 'system')),
    status              text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    idempotency_key     text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
    request_fingerprint text CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    payload             jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(payload) = 'object'),
    result              jsonb CHECK (jsonb_typeof(result) = 'object'),
    attempts            int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts        int NOT NULL CHECK (max_attempts >= 1),
    run_after           timestamptz NOT NULL DEFAULT now(),
    lease_owner         text,
    lease_until         timestamptz,
    heartbeat_at        timestamptz,
    last_error_category text CHECK (last_error_category IN (
                            'provider_unavailable', 'provider_timeout', 'provider_rate_limited', 'provider_rejected',
                            'invalid_output', 'audio_unavailable', 'audio_unreadable', 'no_speech', 'timeout', 'internal')),
    last_error_detail   text CHECK (char_length(last_error_detail) <= 200),
    correlation_id      uuid NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,
    CHECK (CASE WHEN type IN ('purge_meeting', 'purge_account')
                THEN user_id IS NULL AND meeting_id IS NULL AND trigger = 'system'
                ELSE user_id IS NOT NULL AND meeting_id IS NOT NULL END),
    CHECK ((status = 'running') = (lease_owner IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK ((status IN ('succeeded', 'failed', 'canceled')) = (finished_at IS NOT NULL)),
    CHECK ((status = 'succeeded') = (result IS NOT NULL)),
    CHECK (trigger = 'system' OR request_fingerprint IS NOT NULL),
    UNIQUE NULLS NOT DISTINCT (user_id, idempotency_key)
);
CREATE INDEX jobs_ready_idx ON jobs (run_after, created_at) WHERE status = 'queued';
CREATE INDEX jobs_lease_idx ON jobs (lease_until) WHERE status = 'running';
CREATE UNIQUE INDEX jobs_one_active_per_meeting_key ON jobs (meeting_id, type)
    WHERE status IN ('queued', 'running') AND meeting_id IS NOT NULL;
CREATE INDEX jobs_meeting_idx ON jobs (meeting_id, created_at DESC) WHERE meeting_id IS NOT NULL;
CREATE INDEX jobs_user_rate_idx ON jobs (user_id, created_at)
    WHERE trigger = 'user' AND type IN ('generate_notes', 'generate_mom');

-- revision 1 is the only 'original'; UNIQUE (meeting_id, revision_number) therefore
-- makes a duplicate transcribe run unable to write a second original.
-- mutation_fingerprint = sha256hex(canonical request body); a replayed mutation_id with a
-- different body is rejected (422 idempotency_key_reused).
CREATE TABLE transcript_revisions (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id           uuid NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    revision_number      int NOT NULL CHECK (revision_number >= 1),
    kind                 text NOT NULL CHECK (kind IN ('original', 'correction')),
    parent_revision_id   uuid REFERENCES transcript_revisions (id) DEFERRABLE INITIALLY DEFERRED,
    audio_asset_id       uuid REFERENCES audio_assets (id) DEFERRABLE INITIALLY DEFERRED,
    language             text NOT NULL CHECK (char_length(language) BETWEEN 1 AND 16),
    provider             text NOT NULL,
    model                text NOT NULL,
    segments             jsonb NOT NULL CHECK (jsonb_typeof(segments) = 'array'),
    job_id               uuid UNIQUE REFERENCES jobs (id) DEFERRABLE INITIALLY DEFERRED,
    mutation_id          uuid,
    mutation_fingerprint text CHECK (mutation_fingerprint ~ '^[0-9a-f]{64}$'),
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (meeting_id, revision_number),
    UNIQUE (meeting_id, mutation_id),
    CHECK ((kind = 'original') = (revision_number = 1)),
    CHECK ((kind = 'original') = (parent_revision_id IS NULL)),
    CHECK ((kind = 'original') = (job_id IS NOT NULL)),
    CHECK ((kind = 'correction') = (mutation_id IS NOT NULL)),
    CHECK ((mutation_id IS NULL) = (mutation_fingerprint IS NULL))
);

CREATE TABLE documents (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id              uuid NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    kind                    text NOT NULL CHECK (kind IN ('manual_notes', 'ai_notes', 'mom')),
    current_revision_number int NOT NULL DEFAULT 0 CHECK (current_revision_number >= 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (meeting_id, kind)
);

-- Immutable except reviewed_at. Cross-references between a meeting's own rows are
-- DEFERRABLE INITIALLY DEFERRED: cascade order within one DELETE is unspecified, so they are
-- checked at commit, when the whole meeting subtree is gone.
CREATE TABLE document_revisions (
    id                              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id                     uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    revision_number                 int NOT NULL CHECK (revision_number >= 1),
    origin                          text NOT NULL CHECK (origin IN ('ai_generated', 'user_edit', 'restore')),
    schema_version                  int NOT NULL CHECK (schema_version = 1),
    content                         jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object'),
    source_transcript_revision_id   uuid REFERENCES transcript_revisions (id) DEFERRABLE INITIALLY DEFERRED,
    source_manual_notes_revision_id uuid REFERENCES document_revisions (id) DEFERRABLE INITIALLY DEFERRED,
    source_ai_notes_revision_id     uuid REFERENCES document_revisions (id) DEFERRABLE INITIALLY DEFERRED,
    restored_from_revision_id       uuid REFERENCES document_revisions (id) DEFERRABLE INITIALLY DEFERRED,
    job_id                          uuid UNIQUE REFERENCES jobs (id) DEFERRABLE INITIALLY DEFERRED,
    mutation_id                     uuid,
    mutation_fingerprint            text CHECK (mutation_fingerprint ~ '^[0-9a-f]{64}$'),
    provider                        text,
    model                           text,
    prompt_version                  text,
    reviewed_at                     timestamptz,
    created_at                      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (document_id, revision_number),
    UNIQUE (document_id, mutation_id),
    CHECK ((origin = 'ai_generated') = (job_id IS NOT NULL)),
    CHECK ((origin = 'ai_generated') = (provider IS NOT NULL AND model IS NOT NULL AND prompt_version IS NOT NULL)),
    CHECK ((origin = 'ai_generated') = (mutation_id IS NULL)),
    CHECK ((origin = 'restore') = (restored_from_revision_id IS NOT NULL)),
    CHECK ((mutation_id IS NULL) = (mutation_fingerprint IS NULL))
);
CREATE INDEX document_revisions_transcript_idx ON document_revisions (source_transcript_revision_id);

-- Reservation row = ledger entry. Survives meeting deletion (SET NULL) so consumed
-- seconds cannot be refunded by deleting a meeting. period_start is the entitlement
-- period at reserve time, rewritten to the current period at consume (backend.md §9).
CREATE TABLE quota_reservations (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    meeting_id     uuid REFERENCES meetings (id) ON DELETE SET NULL,
    audio_asset_id uuid REFERENCES audio_assets (id) ON DELETE SET NULL,
    job_id         uuid REFERENCES jobs (id) ON DELETE SET NULL,
    seconds        bigint NOT NULL CHECK (seconds > 0),
    period_start   timestamptz NOT NULL,
    status         text NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved', 'consumed', 'released')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    consumed_at    timestamptz,
    released_at    timestamptz,
    CHECK ((status = 'consumed') = (consumed_at IS NOT NULL)),
    CHECK ((status = 'released') = (released_at IS NOT NULL))
);
-- at most one live (reserved or consumed) charge per audio asset
CREATE UNIQUE INDEX quota_reservations_one_live_per_audio_key ON quota_reservations (audio_asset_id)
    WHERE status IN ('reserved', 'consumed');
CREATE INDEX quota_reservations_user_period_idx ON quota_reservations (user_id, period_start)
    WHERE status = 'consumed';
CREATE INDEX quota_reservations_user_reserved_idx ON quota_reservations (user_id)
    WHERE status = 'reserved';

CREATE TABLE exports (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id           uuid NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    document_revision_id uuid NOT NULL REFERENCES document_revisions (id) ON DELETE CASCADE,
    format               text NOT NULL CHECK (format IN ('md', 'whatsapp', 'docx', 'pdf')),
    is_draft             boolean NOT NULL,
    filename             text NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 120),
    content_type         text NOT NULL,
    size_bytes           bigint NOT NULL CHECK (size_bytes > 0),
    sha256               text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key          text NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX exports_meeting_idx ON exports (meeting_id);

-- Plain text of the latest transcript revision and latest revision of each document kind.
CREATE TABLE search_documents (
    meeting_id uuid NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    source     text NOT NULL CHECK (source IN ('transcript', 'manual_notes', 'ai_notes', 'mom')),
    body       text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (meeting_id, source)
);
CREATE INDEX search_documents_body_trgm_idx ON search_documents USING gin (body gin_trgm_ops);

-- One row per provider call (including failures). No content. meeting_id/job_id carry no FK
-- so cost records survive meeting purge; account purge removes them via user_id.
-- cost_usd_micros is computed at insert from the configured prices (backend.md §15).
CREATE TABLE ai_calls (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    meeting_id      uuid,
    job_id          uuid,
    job_type        text NOT NULL CHECK (job_type IN ('transcribe', 'generate_notes', 'generate_mom')),
    attempt         int NOT NULL CHECK (attempt >= 1),
    capability      text NOT NULL CHECK (capability IN ('stt', 'llm')),
    provider        text NOT NULL,
    model           text NOT NULL,
    prompt_version  text,
    input_tokens    int CHECK (input_tokens >= 0),
    output_tokens   int CHECK (output_tokens >= 0),
    audio_ms        bigint CHECK (audio_ms >= 0),
    latency_ms      int NOT NULL CHECK (latency_ms >= 0),
    cost_usd_micros bigint NOT NULL DEFAULT 0 CHECK (cost_usd_micros >= 0),
    outcome         text NOT NULL CHECK (outcome IN ('ok', 'error')),
    error_category  text,
    correlation_id  uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((capability = 'llm') = (prompt_version IS NOT NULL))
);
CREATE INDEX ai_calls_created_idx ON ai_calls (created_at);
CREATE INDEX ai_calls_user_idx ON ai_calls (user_id);

-- Product events (PRD §10). Names only, no content. Dedup by (user_id, event_id).
-- meeting_id carries no FK (same reason as ai_calls); account purge removes rows.
CREATE TABLE events (
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id    uuid NOT NULL,
    name        text NOT NULL CHECK (name IN (
                    'onboarding_completed', 'recording_started', 'recording_interrupted', 'recording_saved',
                    'upload_verified', 'transcription_completed', 'notes_ready', 'mom_generated',
                    'document_saved', 'mom_reviewed', 'export_completed', 'share_sheet_opened', 'quota_blocked')),
    source      text NOT NULL CHECK (source IN ('client', 'server')),
    meeting_id  uuid,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event_id)
);
CREATE INDEX events_name_time_idx ON events (name, occurred_at);

-- Cost per audio hour (PRD §9 observability), per UTC day.
CREATE VIEW daily_ai_cost AS
WITH cost AS (
    SELECT date_trunc('day', created_at AT TIME ZONE 'UTC') AS day, sum(cost_usd_micros) AS cost_usd_micros
    FROM ai_calls
    GROUP BY 1
), audio AS (
    SELECT date_trunc('day', consumed_at AT TIME ZONE 'UTC') AS day, sum(seconds) AS consumed_seconds
    FROM quota_reservations
    WHERE status = 'consumed'
    GROUP BY 1
)
SELECT COALESCE(c.day, a.day)            AS day,
       COALESCE(c.cost_usd_micros, 0)    AS cost_usd_micros,
       COALESCE(a.consumed_seconds, 0)   AS consumed_seconds,
       CASE WHEN a.consumed_seconds > 0
            THEN COALESCE(c.cost_usd_micros, 0)::numeric / 1e6 / (a.consumed_seconds / 3600.0)
       END                               AS usd_per_audio_hour
FROM cost c
FULL JOIN audio a ON a.day = c.day;
