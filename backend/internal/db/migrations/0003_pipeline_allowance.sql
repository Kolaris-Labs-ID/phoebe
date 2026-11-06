-- Revisions written before this migration carry provider segment timestamps (whisper-1 / fake).
ALTER TABLE transcript_revisions ADD COLUMN timing text NOT NULL DEFAULT 'segment' CHECK (timing IN ('segment', 'chunk'));
ALTER TABLE transcript_revisions ALTER COLUMN timing DROP DEFAULT;

ALTER TABLE meetings DROP CONSTRAINT meetings_pipeline_state_check;
ALTER TABLE meetings ADD CONSTRAINT meetings_pipeline_state_check CHECK (pipeline_state IN (
    'awaiting_audio', 'uploading', 'uploaded', 'transcribing', 'transcription_failed', 'transcript_ready',
    'generating_notes', 'notes_failed', 'notes_ready'));

ALTER TABLE upload_sessions ADD COLUMN transcribe_notes text CHECK (transcribe_notes IN ('auto', 'skip'));
UPDATE upload_sessions SET transcribe_notes = 'auto' WHERE transcribe_idempotency_key IS NOT NULL;
ALTER TABLE upload_sessions ADD CHECK ((transcribe_idempotency_key IS NULL) = (transcribe_notes IS NULL));

-- One row per AI generation job (automatic notes, user notes generate/regenerate, MoM).
-- job_id = the generate_notes/generate_mom job; transcribe_job_id = the transcribe job that
-- reserved the slot for its automatic notes (job_id is attached when that notes job is enqueued).
-- Both unique: a job never holds two slots. Rows survive meeting purge (SET NULL) so consumed
-- generations keep counting in the window.
CREATE TABLE ai_generation_slots (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    meeting_id        uuid REFERENCES meetings (id) ON DELETE SET NULL,
    job_id            uuid UNIQUE REFERENCES jobs (id) ON DELETE SET NULL,
    transcribe_job_id uuid UNIQUE REFERENCES jobs (id) ON DELETE SET NULL,
    status            text NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved', 'consumed', 'released')),
    reserved_at       timestamptz NOT NULL DEFAULT now(),
    consumed_at       timestamptz,
    released_at       timestamptz,
    CHECK ((status = 'consumed') = (consumed_at IS NOT NULL)),
    CHECK ((status = 'released') = (released_at IS NOT NULL))
);
CREATE INDEX ai_generation_slots_user_window_idx ON ai_generation_slots (user_id, reserved_at)
    WHERE status IN ('reserved', 'consumed');
CREATE INDEX ai_generation_slots_meeting_idx ON ai_generation_slots (meeting_id) WHERE status = 'reserved';

DROP INDEX jobs_user_rate_idx;
