import Foundation

enum StepState: Equatable {
    case pending, waitingConnection, waitingQuota, planExpired
    case active(percent: Int?)
    case verifying, done, failed
}

struct JobSnapshot: Equatable {
    let status: JobStatus
    let errorCategory: String?
    let updatedAt: Date
}

struct StageInputs: Equatable {
    var localPhase: LocalPhase = .none
    var pipeline: PipelineState?
    var uploadState: PendingUploadState?
    var uploadProgress: Double?
    var serverHasAudio = false
    var hasTranscript = false
    var hasAINotes = false
    var transcribeJob: JobSnapshot?
    var notesJob: JobSnapshot?
}

enum DetailStatus {
    private static let pastUpload: Set<PipelineState> = [.uploaded, .transcribing, .transcriptionFailed, .generatingNotes, .notesFailed, .notesReady]

    static func upload(_ s: StageInputs) -> StepState {
        if s.uploadState == .failed || s.uploadState == .rejected || s.localPhase == .rejected { return .failed }
        if s.serverHasAudio || s.hasTranscript || s.uploadState == .verified { return .done }
        if let pipeline = s.pipeline, pastUpload.contains(pipeline) { return .done }
        switch s.localPhase {
        case .waitingQuota, .waitingEntitlement: return .done
        case .waitingConnection: return .waitingConnection
        case .uploading: return .active(percent: s.uploadProgress.map { Int(($0 * 100).rounded()) })
        case .verifying: return .verifying
        default: break
        }
        return s.pipeline == .uploading ? .active(percent: nil) : .pending
    }

    static func transcript(_ s: StageInputs) -> StepState {
        if s.hasTranscript { return .done }
        if s.transcribeJob?.status.isActive == true || s.pipeline == .transcribing { return .active(percent: nil) }
        if s.pipeline == .transcriptionFailed || s.transcribeJob?.status == .failed { return .failed }
        switch s.localPhase {
        case .waitingQuota: return .waitingQuota
        case .waitingEntitlement: return .planExpired
        default: return .pending
        }
    }

    static func notes(_ s: StageInputs) -> StepState {
        if s.notesJob?.status.isActive == true || s.pipeline == .generatingNotes { return .active(percent: nil) }
        if s.pipeline == .notesFailed || s.notesJob?.status == .failed { return .failed }
        return s.hasAINotes ? .done : .pending
    }

    static func isNoSpeech(_ s: StageInputs) -> Bool {
        !s.hasTranscript && s.transcribeJob?.status == .failed && s.transcribeJob?.errorCategory == "no_speech"
    }
}

struct FailureCopy: Equatable {
    let key: String
    let retryAllowed: Bool
    let offerDelete: Bool
    let showsRequestCode: Bool

    static func make(category: String?) -> FailureCopy {
        switch category {
        case "no_speech": FailureCopy(key: "detail.noSpeech", retryAllowed: false, offerDelete: false, showsRequestCode: false)
        case "audio_unreadable": FailureCopy(key: "fail.audioUnreadable", retryAllowed: false, offerDelete: true, showsRequestCode: false)
        case "audio_unavailable": FailureCopy(key: "detail.player.expired", retryAllowed: false, offerDelete: false, showsRequestCode: false)
        case "provider_rejected": FailureCopy(key: "fail.rejected", retryAllowed: false, offerDelete: false, showsRequestCode: true)
        case "invalid_output": FailureCopy(key: "fail.invalidOutput", retryAllowed: true, offerDelete: false, showsRequestCode: false)
        case "internal": FailureCopy(key: "fail.internal", retryAllowed: true, offerDelete: false, showsRequestCode: true)
        default: FailureCopy(key: "fail.provider", retryAllowed: true, offerDelete: false, showsRequestCode: false)
        }
    }
}

enum UploadRejectCopy {
    static func key(for code: String?) -> String {
        switch code {
        case "audio_unsupported": "import.error.type"
        case "audio_corrupt": "import.error.corrupt"
        case "audio_empty": "import.error.empty"
        case "audio_too_long": "import.error.tooLong"
        case "upload_too_large", "payload_too_large": "import.error.tooBig"
        case "audio_unavailable": "detail.player.expired"
        default: "import.error.corrupt"
        }
    }
}

enum GenerationBlock: Equatable {
    case offline, syncFirst, noSpeech, needsNotes, transcriptNotReady, inProgress
    case rateLimited(until: Date?)

    var key: String {
        switch self {
        case .offline: "detail.ai.offline"
        case .syncFirst: "doc.regen.syncFirst"
        case .noSpeech: "detail.noSpeech"
        case .needsNotes: "mom.needsNotes"
        case .transcriptNotReady: "detail.notes.needsTranscript"
        case .inProgress: "detail.ai.inProgress"
        case .rateLimited: "doc.regen.limited"
        }
    }
}

struct EditSnapshot: Equatable {
    var hasPendingWork: Bool
    var inFlight: Bool
    var conflict: Bool
    var failed: Bool
}

enum GenerationGate {
    static func notes(online: Bool, noSpeech: Bool, hasTranscript: Bool, jobActive: Bool, manualEdit: EditSnapshot?, rateLimitedUntil: Date?, now: Date = .now) -> GenerationBlock? {
        if noSpeech { return .noSpeech }
        if !hasTranscript { return .transcriptNotReady }
        if jobActive { return .inProgress }
        if !online { return .offline }
        if let edit = manualEdit, edit.conflict || edit.failed { return .syncFirst }
        if let until = rateLimitedUntil, until > now { return .rateLimited(until: until) }
        return nil
    }

    static func mom(online: Bool, noSpeech: Bool, hasAINotes: Bool, jobActive: Bool, edits: [EditSnapshot], rateLimitedUntil: Date?, now: Date = .now) -> GenerationBlock? {
        if noSpeech { return .noSpeech }
        if !hasAINotes { return .needsNotes }
        if jobActive { return .inProgress }
        if !online { return .offline }
        if edits.contains(where: { $0.conflict || $0.failed }) { return .syncFirst }
        if let until = rateLimitedUntil, until > now { return .rateLimited(until: until) }
        return nil
    }

    static func rateLimitedUntil(usage: UsageDTO?, retryAfter: Date?) -> Date? {
        [usage?.aiGenerations.nextAvailableAt, retryAfter].compactMap { $0 }.max()
    }
}

enum PlaybackAvailability: Equatable {
    case local(URL), cloud, expired, offline, none

    var isPlayable: Bool {
        switch self {
        case .local, .cloud: true
        default: false
        }
    }

    static func make(audioDeletedReason: String?, audioAvailable: Bool, localFile: URL?, isOnline: Bool) -> PlaybackAvailability {
        if audioDeletedReason != nil { return .expired }
        if let localFile { return .local(localFile) }
        if !audioAvailable { return .none }
        return isOnline ? .cloud : .offline
    }
}

enum NotesSaveState {
    static func make(edit: EditSnapshot?, debouncing: Bool, online: Bool) -> SaveState {
        if debouncing { return .saving }
        guard let edit, edit.hasPendingWork || edit.conflict || edit.failed else { return .synced }
        if edit.failed || edit.conflict { return .notSynced }
        if !online { return .offline }
        return edit.inFlight ? .syncing : .local
    }
}

enum MetadataRules {
    static let maxTitle = 200

    static func languageEditable(localPhase: LocalPhase, pipeline: PipelineState?, transcribeJob: JobSnapshot?, hasTranscript: Bool) -> Bool {
        if hasTranscript { return false }
        if localPhase == .uploading || localPhase == .verifying { return false }
        if let job = transcribeJob, job.status.isActive || job.status == .succeeded { return false }
        switch pipeline {
        case nil, .awaitingAudio?, .uploaded?, .transcriptionFailed?: return true
        default: return false
        }
    }

    static func cleanParticipants(_ list: [String]) -> [String] {
        list.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
    }

    static func patch(
        baseRevision: Int,
        original: (title: String, startedAt: Date?, timezone: String, language: TranscriptionLanguage, participants: [String]),
        edited: (title: String, startedAt: Date?, timezone: String, language: TranscriptionLanguage, participants: [String])
    ) -> MeetingPatchRequest {
        var p = MeetingPatchRequest()
        let title = edited.title.trimmingCharacters(in: .whitespacesAndNewlines)
        if title != original.title { p.title = .set(title) }
        if edited.startedAt != original.startedAt { p.startedAt = edited.startedAt.map { .set($0) } ?? .clear }
        if edited.timezone != original.timezone { p.timezone = .set(edited.timezone) }
        if edited.language != original.language { p.language = .set(edited.language) }
        let participants = cleanParticipants(edited.participants)
        if participants != original.participants { p.participants = .set(participants) }
        if p.hasMetadataChanges { p.expectedMetadataRevision = baseRevision }
        return p
    }
}

enum NotePointEditing {
    // A typed Return arrives as "\n" inside the vertical TextField; split there into a new point.
    static func applyEdit(_ items: [NoteItem], id: String, text: String) -> (items: [NoteItem], focus: String?) {
        guard let index = items.firstIndex(where: { $0.id == id }) else { return (items, nil) }
        var result = items
        guard let newline = text.firstIndex(of: "\n") else {
            result[index].text = text
            return (result, nil)
        }
        result[index].text = String(text[..<newline])
        let rest = String(text[text.index(after: newline)...]).replacingOccurrences(of: "\n", with: " ")
        let item = NoteItem.new(text: rest)
        result.insert(item, at: index + 1)
        return (result, item.id)
    }
}

enum TranscriptDiff {
    static func correctedIDs(current: [TranscriptSegment], original: [TranscriptSegment]) -> Set<String> {
        let originalText = Dictionary(original.map { ($0.id, $0.text) }, uniquingKeysWith: { a, _ in a })
        return Set(current.filter { originalText[$0.id] != nil && originalText[$0.id] != $0.text }.map(\.id))
    }

    static func playingSegmentID(_ segments: [TranscriptSegment], atMs ms: Int) -> String? {
        segments.last(where: { $0.startMs <= ms && ms < max($0.endMs, $0.startMs + 1) })?.id
    }
}
