-- C3 sessions: a session is a token family with an absolute expiry (created_at + SESSION_MAX_AGE,
-- never extended). Each row of session_tokens is one access + refresh pair; refresh is single-use.
-- Pre-release: old opaque session tokens become invalid (no compat).

DELETE FROM sessions;
ALTER TABLE sessions DROP COLUMN token_hash;
ALTER TABLE sessions ADD COLUMN revoked_reason text
    CHECK (revoked_reason IN ('logout', 'refresh_reuse', 'account_deleted'));
ALTER TABLE sessions ADD CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL));

CREATE TABLE session_tokens (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id         uuid NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    access_token_hash  bytea NOT NULL UNIQUE CHECK (octet_length(access_token_hash) = 32),
    access_expires_at  timestamptz NOT NULL,
    -- first successful authentication with the access token; a used successor disables the reuse grace
    access_used_at     timestamptz,
    refresh_token_hash bytea NOT NULL UNIQUE CHECK (octet_length(refresh_token_hash) = 32),
    created_at         timestamptz NOT NULL DEFAULT now(),
    rotated_at         timestamptz,
    successor_id       uuid REFERENCES session_tokens (id) ON DELETE SET NULL,
    -- unused successor dropped when its predecessor was replayed within REFRESH_REUSE_GRACE
    invalidated_at     timestamptz
);
CREATE INDEX session_tokens_session_idx ON session_tokens (session_id);
CREATE INDEX session_tokens_successor_idx ON session_tokens (successor_id) WHERE successor_id IS NOT NULL;
