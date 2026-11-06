-- The auto-start outcome of an upload completion, stored at finalize so a replayed complete
-- returns the first response's job and transcription_blocked instead of recomputing them.
ALTER TABLE upload_sessions
    ADD COLUMN transcribe_job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,
    ADD COLUMN transcription_blocked text CHECK (transcription_blocked IN ('ai_generation_limit')),
    ADD COLUMN transcription_blocked_next_slot_at timestamptz,
    ADD CHECK (transcription_blocked IS NOT NULL OR transcription_blocked_next_slot_at IS NULL),
    ADD CHECK (transcription_blocked IS NULL OR transcribe_job_id IS NULL);

UPDATE upload_sessions s SET transcribe_job_id = j.id
FROM jobs j
WHERE s.status = 'completed' AND j.user_id = s.user_id AND j.idempotency_key = s.transcribe_idempotency_key::text;
