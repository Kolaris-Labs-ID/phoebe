import Foundation
import Testing
@testable import Phoebe

struct MeetingDetailFeatureTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func job(_ status: JobStatus, _ category: String? = nil) -> JobSnapshot {
        JobSnapshot(status: status, errorCategory: category, updatedAt: now)
    }

    // MARK: Pipeline strip

    @Test func uploadStepFollowsLocalPhaseThenServer() {
        #expect(DetailStatus.upload(StageInputs(localPhase: .queued)) == .pending)
        #expect(DetailStatus.upload(StageInputs(localPhase: .waitingConnection)) == .waitingConnection)
        #expect(DetailStatus.upload(StageInputs(localPhase: .uploading, uploadProgress: 0.454)) == .active(percent: 45))
        #expect(DetailStatus.upload(StageInputs(localPhase: .verifying)) == .verifying)
        #expect(DetailStatus.upload(StageInputs(localPhase: .rejected)) == .failed)
        #expect(DetailStatus.upload(StageInputs(uploadState: .failed)) == .failed)
        #expect(DetailStatus.upload(StageInputs(localPhase: .waitingQuota)) == .done)
        #expect(DetailStatus.upload(StageInputs(pipeline: .transcribing)) == .done)
        #expect(DetailStatus.upload(StageInputs(localPhase: .verifying, pipeline: .transcribing)) == .done)
    }

    @Test func transcriptStepShowsQuotaPlanAndFailure() {
        #expect(DetailStatus.transcript(StageInputs(localPhase: .waitingQuota)) == .waitingQuota)
        #expect(DetailStatus.transcript(StageInputs(localPhase: .waitingEntitlement)) == .planExpired)
        #expect(DetailStatus.transcript(StageInputs(transcribeJob: job(.running))) == .active(percent: nil))
        #expect(DetailStatus.transcript(StageInputs(pipeline: .transcriptionFailed)) == .failed)
        #expect(DetailStatus.transcript(StageInputs(hasTranscript: true, transcribeJob: job(.failed))) == .done)
    }

    @Test func notesStepFailsIndependently() {
        #expect(DetailStatus.notes(StageInputs(hasTranscript: true)) == .pending)
        #expect(DetailStatus.notes(StageInputs(pipeline: .generatingNotes)) == .active(percent: nil))
        #expect(DetailStatus.notes(StageInputs(pipeline: .notesFailed, hasTranscript: true)) == .failed)
        #expect(DetailStatus.notes(StageInputs(hasAINotes: true)) == .done)
        #expect(DetailStatus.transcript(StageInputs(pipeline: .notesFailed, hasTranscript: true)) == .done)
    }

    @Test func noSpeechDetected() {
        #expect(DetailStatus.isNoSpeech(StageInputs(transcribeJob: job(.failed, "no_speech"))))
        #expect(!DetailStatus.isNoSpeech(StageInputs(transcribeJob: job(.failed, "internal"))))
    }

    // MARK: Failure categories

    @Test func failureCategoriesControlRetry() {
        #expect(FailureCopy.make(category: "no_speech").retryAllowed == false)
        #expect(FailureCopy.make(category: "audio_unreadable") == FailureCopy(key: "fail.audioUnreadable", retryAllowed: false, offerDelete: true, showsRequestCode: false))
        #expect(FailureCopy.make(category: "audio_unavailable").key == "detail.player.expired")
        for c in ["provider_unavailable", "provider_timeout", "provider_rate_limited", "timeout"] {
            #expect(FailureCopy.make(category: c) == FailureCopy(key: "fail.provider", retryAllowed: true, offerDelete: false, showsRequestCode: false))
        }
        #expect(FailureCopy.make(category: "provider_rejected").retryAllowed == false)
        #expect(FailureCopy.make(category: "provider_rejected").showsRequestCode)
        #expect(FailureCopy.make(category: "invalid_output").retryAllowed)
        #expect(FailureCopy.make(category: "internal").showsRequestCode)
        #expect(UploadRejectCopy.key(for: "audio_too_long") == "import.error.tooLong")
        #expect(UploadRejectCopy.key(for: "audio_unsupported") == "import.error.type")
    }

    // MARK: Generation gating

    @Test func notesGenerationGate() {
        let clean = EditSnapshot(hasPendingWork: false, inFlight: false, conflict: false, failed: false)
        let conflicted = EditSnapshot(hasPendingWork: true, inFlight: false, conflict: true, failed: false)
        #expect(GenerationGate.notes(online: true, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: clean, rateLimitedUntil: nil, now: now) == nil)
        #expect(GenerationGate.notes(online: false, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: nil, rateLimitedUntil: nil, now: now) == .offline)
        #expect(GenerationGate.notes(online: true, noSpeech: true, hasTranscript: false, jobActive: false, manualEdit: nil, rateLimitedUntil: nil, now: now) == .noSpeech)
        #expect(GenerationGate.notes(online: true, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: conflicted, rateLimitedUntil: nil, now: now) == .syncFirst)
        let later = now.addingTimeInterval(60)
        #expect(GenerationGate.notes(online: true, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: nil, rateLimitedUntil: later, now: now) == .rateLimited(until: later))
        #expect(GenerationGate.notes(online: true, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: nil, rateLimitedUntil: now.addingTimeInterval(-1), now: now) == nil)
    }

    @Test func momGenerationNeedsAINotesAndSyncedEdits() {
        let failed = EditSnapshot(hasPendingWork: true, inFlight: false, conflict: false, failed: true)
        #expect(GenerationGate.mom(online: true, noSpeech: false, hasAINotes: false, jobActive: false, edits: [], rateLimitedUntil: nil, now: now) == .needsNotes)
        #expect(GenerationGate.mom(online: true, noSpeech: false, hasAINotes: true, jobActive: false, edits: [failed], rateLimitedUntil: nil, now: now) == .syncFirst)
        #expect(GenerationGate.mom(online: true, noSpeech: false, hasAINotes: true, jobActive: true, edits: [], rateLimitedUntil: nil, now: now) == .inProgress)
        #expect(GenerationGate.mom(online: true, noSpeech: false, hasAINotes: true, jobActive: false, edits: [], rateLimitedUntil: nil, now: now) == nil)
        #expect(GenerationGate.rateLimitedUntil(usage: nil, retryAfter: now) == now)
    }

    // MARK: Playback

    @Test func playbackAvailability() {
        let file = URL(fileURLWithPath: "/tmp/a.m4a")
        #expect(PlaybackAvailability.make(audioDeletedReason: "user", audioAvailable: false, localFile: file, isOnline: true) == .expired)
        #expect(PlaybackAvailability.make(audioDeletedReason: "expired", audioAvailable: false, localFile: nil, isOnline: true) == .expired)
        #expect(PlaybackAvailability.make(audioDeletedReason: nil, audioAvailable: false, localFile: file, isOnline: false) == .local(file))
        #expect(PlaybackAvailability.make(audioDeletedReason: nil, audioAvailable: true, localFile: nil, isOnline: true) == .cloud)
        #expect(PlaybackAvailability.make(audioDeletedReason: nil, audioAvailable: true, localFile: nil, isOnline: false) == .offline)
        #expect(PlaybackAvailability.make(audioDeletedReason: nil, audioAvailable: false, localFile: nil, isOnline: true) == .none)
        #expect(!PlaybackAvailability.expired.isPlayable)
    }

    // MARK: Save indicator

    @Test func manualNotesSaveState() {
        #expect(NotesSaveState.make(edit: nil, debouncing: true, online: true) == .saving)
        #expect(NotesSaveState.make(edit: nil, debouncing: false, online: true) == .synced)
        let pending = EditSnapshot(hasPendingWork: true, inFlight: false, conflict: false, failed: false)
        #expect(NotesSaveState.make(edit: pending, debouncing: false, online: false) == .offline)
        #expect(NotesSaveState.make(edit: pending, debouncing: false, online: true) == .local)
        #expect(NotesSaveState.make(edit: EditSnapshot(hasPendingWork: true, inFlight: true, conflict: false, failed: false), debouncing: false, online: true) == .syncing)
        #expect(NotesSaveState.make(edit: EditSnapshot(hasPendingWork: false, inFlight: false, conflict: true, failed: false), debouncing: false, online: true) == .notSynced)
    }

    // MARK: Metadata

    @Test func metadataPatchSendsChangedFieldsWithExpectedRevision() throws {
        let date = now
        let original = (title: "Weekly", startedAt: Optional(date), timezone: "Asia/Jakarta", language: TranscriptionLanguage.auto, participants: ["Budi"])
        let unchanged = MetadataRules.patch(baseRevision: 3, original: original, edited: original)
        #expect(unchanged.isEmpty)
        #expect(unchanged.expectedMetadataRevision == nil)

        var edited = original
        edited.title = "  Weekly sync  "
        edited.participants = ["Budi", " ", " Sari "]
        let patch = MetadataRules.patch(baseRevision: 3, original: original, edited: edited)
        #expect(patch.title == .set("Weekly sync"))
        #expect(patch.participants == .set(["Budi", "Sari"]))
        #expect(patch.startedAt == .unchanged)
        #expect(patch.expectedMetadataRevision == 3)

        var cleared = original
        cleared.startedAt = nil
        let json = try #require(String(data: APICoding.encode(MetadataRules.patch(baseRevision: 4, original: original, edited: cleared)), encoding: .utf8))
        #expect(json == #"{"expected_metadata_revision":4,"started_at":null}"#)
    }

    @Test func languageLockedOnceUploadOrTranscriptionStarted() {
        #expect(MetadataRules.languageEditable(localPhase: .queued, pipeline: nil, transcribeJob: nil, hasTranscript: false))
        #expect(MetadataRules.languageEditable(localPhase: .none, pipeline: .transcriptionFailed, transcribeJob: job(.failed), hasTranscript: false))
        #expect(!MetadataRules.languageEditable(localPhase: .uploading, pipeline: nil, transcribeJob: nil, hasTranscript: false))
        #expect(!MetadataRules.languageEditable(localPhase: .none, pipeline: .transcribing, transcribeJob: job(.running), hasTranscript: false))
        #expect(!MetadataRules.languageEditable(localPhase: .none, pipeline: .notesReady, transcribeJob: job(.succeeded), hasTranscript: true))
    }

    // MARK: Manual notes & transcript

    @Test func returnSplitsPointKeepingIdsStable() {
        let items = [NoteItem(id: "n1", text: "Budget", atMs: 1000), NoteItem(id: "n2", text: "Vendor", atMs: nil)]
        let plain = NotePointEditing.applyEdit(items, id: "n1", text: "Budget Q1")
        #expect(plain.items.map(\.id) == ["n1", "n2"])
        #expect(plain.items[0].text == "Budget Q1")
        #expect(plain.items[0].atMs == 1000)
        #expect(plain.focus == nil)

        let split = NotePointEditing.applyEdit(items, id: "n1", text: "Budget\nDisetujui")
        #expect(split.items.count == 3)
        #expect(split.items[0] == NoteItem(id: "n1", text: "Budget", atMs: 1000))
        #expect(split.items[1].text == "Disetujui")
        #expect(split.items[1].id.hasPrefix("h_"))
        #expect(split.focus == split.items[1].id)
        #expect(split.items[2].id == "n2")
    }

    @Test func correctedSegmentsAndPlayingSegment() {
        let original = [TranscriptSegment(id: "s1", startMs: 0, endMs: 4000, text: "Halo"), TranscriptSegment(id: "s2", startMs: 4000, endMs: 9000, text: "Budget 50")]
        var current = original
        current[1].text = "Budget 50 juta"
        #expect(TranscriptDiff.correctedIDs(current: current, original: original) == ["s2"])
        #expect(TranscriptDiff.playingSegmentID(current, atMs: 4500) == "s2")
        #expect(TranscriptDiff.playingSegmentID(current, atMs: 100) == "s1")
        #expect(TranscriptDiff.playingSegmentID(current, atMs: 20_000) == nil)
    }

    @Test func pipelineStepCopyUsesStateWords() {
        #expect(PipelineStep.stateKey(.upload, .done) == "step.done")
        #expect(PipelineStep.stateKey(.transcript, .done) == "step.ready")
        #expect(PipelineStep.upload.icon(for: .failed) == "exclamationmark.icloud")
        #expect(PipelineStep.transcript.icon(for: .waitingQuota) == "gauge.with.dots.needle.100percent")
        #expect(PipelineStep.notes.icon(for: .active(percent: nil)) == "sparkles")
    }

    @Test func meetingDetailStringsExistInBothLanguages() throws {
        let en = try #require(strings(table: "MeetingDetail", localization: "en"))
        let id = try #require(strings(table: "MeetingDetail", localization: "id"))
        #expect(Set(en.keys) == Set(id.keys))
        #expect(en.values.allSatisfy { !$0.isEmpty } && id.values.allSatisfy { !$0.isEmpty })
        #expect(id["doc.outdated.chip"] == "Berdasarkan versi lama")
        #expect(en["doc.outdated.chip"] == "Based on earlier version")
    }
}
