import Foundation
import SwiftData

// iOS 17 SwiftData rules: enums stored as raw String with typed accessors; no Codable arrays of
// mutable structs as attributes; no predicates across to-many relationships.

enum LocalPhase: String, Sendable, CaseIterable {
    case none, recording, merging, waitingStorage, waitingConnection, queued, uploading, verifying
    case waitingQuota, waitingEntitlement, rejected, orphaned
}

enum LocalRecordingState: String, Sendable, CaseIterable {
    case recording, paused, interrupted, stopped, merged, queued
}

enum PendingUploadState: String, Sendable, CaseIterable {
    case waiting, creating, uploading, completing, verified, failed, rejected
}

enum IntentType: String, Sendable, CaseIterable {
    case transcribe
    case generateNotes = "generate_notes"
    case generateMom = "generate_mom"
}

enum MutationKind: String, Sendable, CaseIterable {
    case meetingCreate, meetingPatch, meetingDelete, audioDelete
    case folderCreate, folderRename, folderDelete
    case tagCreate, tagRename, tagDelete
}

@Model
final class UserProfile {
    @Attribute(.unique) var id: UUID
    var displayName: String
    var language: String
    var timezone: String
    var defaultTranscriptionLanguage: String = "auto"
    var deleteLocalAudioAfterTranscript: Bool = false

    init(id: UUID, displayName: String, language: String, timezone: String) {
        self.id = id
        self.displayName = displayName
        self.language = language
        self.timezone = timezone
    }

    var transcriptionLanguage: TranscriptionLanguage {
        get { TranscriptionLanguage(rawValue: defaultTranscriptionLanguage) ?? .auto }
        set { defaultTranscriptionLanguage = newValue.rawValue }
    }
}

@Model
final class Folder {
    @Attribute(.unique) var id: UUID
    var name: String
    var meetingCount: Int = 0
    var updatedAt: Date
    var isServerCreated: Bool = false
    @Relationship(deleteRule: .nullify, inverse: \Meeting.folder) var meetings: [Meeting] = []

    init(id: UUID = UUID(), name: String, updatedAt: Date = .now, isServerCreated: Bool = false) {
        self.id = id
        self.name = name
        self.updatedAt = updatedAt
        self.isServerCreated = isServerCreated
    }
}

@Model
final class Tag {
    @Attribute(.unique) var id: UUID
    var name: String
    var meetingCount: Int = 0
    var updatedAt: Date
    var isServerCreated: Bool = false
    @Relationship(deleteRule: .nullify, inverse: \Meeting.tags) var meetings: [Meeting] = []

    init(id: UUID = UUID(), name: String, updatedAt: Date = .now, isServerCreated: Bool = false) {
        self.id = id
        self.name = name
        self.updatedAt = updatedAt
        self.isServerCreated = isServerCreated
    }
}

@Model
final class Meeting {
    @Attribute(.unique) var id: UUID
    var sourceRaw: String
    var title: String
    var startedAt: Date?
    var createdAt: Date
    var timezone: String
    var language: String
    var participants: [String] = []
    var metadataRevision: Int = 1
    var folder: Folder?
    var tags: [Tag] = []
    var searchText: String = ""
    var pipelineState: String?
    var activeJobTypes: [String] = []
    var failedStage: String?
    var transcriptionErrorCategory: String?
    var localPhaseRaw: String = LocalPhase.none.rawValue
    var rejectCode: String?
    var metadataConflict: Bool = false
    var durationMs: Int?
    var audioAvailable: Bool = false
    var audioDeletedReason: String?
    var hasTranscript: Bool = false
    var momReviewStatus: String?
    var isServerCreated: Bool = false
    var updatedAt: Date?

    init(
        id: UUID = UUID(),
        source: MeetingSource,
        title: String = "",
        startedAt: Date? = .now,
        timezone: String = TimeZone.current.identifier,
        language: TranscriptionLanguage = .auto
    ) {
        self.id = id
        self.sourceRaw = source.rawValue
        self.title = title
        self.startedAt = startedAt
        self.createdAt = .now
        self.timezone = timezone
        self.language = language.rawValue
        self.searchText = title.lowercased()
    }

    var source: MeetingSource { MeetingSource(rawValue: sourceRaw) ?? .recording }

    var localPhase: LocalPhase {
        get { LocalPhase(rawValue: localPhaseRaw) ?? .none }
        set { localPhaseRaw = newValue.rawValue }
    }

    var transcriptionLanguage: TranscriptionLanguage {
        get { TranscriptionLanguage(rawValue: language) ?? .auto }
        set { language = newValue.rawValue }
    }

    var pipeline: PipelineState? { pipelineState.flatMap(PipelineState.init(rawValue:)) }
    var activeJobs: [JobType] { activeJobTypes.compactMap(JobType.init(rawValue:)) }
    var momReview: ReviewStatus? { momReviewStatus.flatMap(ReviewStatus.init(rawValue:)) }
    var sortDate: Date { startedAt ?? createdAt }

    func refreshSearchText() {
        searchText = ([title] + tags.map(\.name)).joined(separator: " ").lowercased()
    }
}

@Model
final class TranscriptRevision {
    @Attribute(.unique) var id: UUID
    var meetingID: UUID
    var revisionNumber: Int
    var kindRaw: String
    var language: String
    var createdAt: Date
    var segmentsJSON: Data

    init(id: UUID, meetingID: UUID, revisionNumber: Int, kind: TranscriptKind, language: String, createdAt: Date, segmentsJSON: Data) {
        self.id = id
        self.meetingID = meetingID
        self.revisionNumber = revisionNumber
        self.kindRaw = kind.rawValue
        self.language = language
        self.createdAt = createdAt
        self.segmentsJSON = segmentsJSON
    }

    var kind: TranscriptKind { TranscriptKind(rawValue: kindRaw) ?? .original }

    func segments() throws -> [TranscriptSegment] {
        try APICoding.decoder().decode([TranscriptSegment].self, from: segmentsJSON)
    }
}

@Model
final class Document {
    @Attribute(.unique) var id: UUID
    var meetingID: UUID
    var kindRaw: String
    var currentRevisionNumber: Int
    var currentRevisionID: UUID
    var reviewStatusRaw: String
    var isStale: Bool = false
    var staleReasons: [String] = []
    var updatedAt: Date

    init(id: UUID, meetingID: UUID, kind: DocumentKind, currentRevisionNumber: Int, currentRevisionID: UUID, reviewStatus: ReviewStatus, updatedAt: Date) {
        self.id = id
        self.meetingID = meetingID
        self.kindRaw = kind.rawValue
        self.currentRevisionNumber = currentRevisionNumber
        self.currentRevisionID = currentRevisionID
        self.reviewStatusRaw = reviewStatus.rawValue
        self.updatedAt = updatedAt
    }

    var kind: DocumentKind { DocumentKind(rawValue: kindRaw) ?? .manualNotes }

    var reviewStatus: ReviewStatus {
        get { ReviewStatus(rawValue: reviewStatusRaw) ?? .draft }
        set { reviewStatusRaw = newValue.rawValue }
    }

    func apply(_ dto: DocumentSummaryDTO) {
        currentRevisionNumber = dto.currentRevisionNumber
        currentRevisionID = dto.currentRevisionId
        reviewStatus = dto.reviewStatus
        isStale = dto.isStale
        staleReasons = dto.staleReasons
        updatedAt = dto.updatedAt
    }
}

@Model
final class DocumentRevision {
    @Attribute(.unique) var id: UUID
    var meetingID: UUID
    var kindRaw: String
    var revisionNumber: Int
    var originRaw: String
    var restoredFromRevisionNumber: Int?
    var reviewedAt: Date?
    var sourceTranscriptRevisionID: UUID?
    var sourceTranscriptRevisionNumber: Int?
    var sourceManualNotesRevisionID: UUID?
    var sourceManualNotesRevisionNumber: Int?
    var sourceAINotesRevisionID: UUID?
    var sourceAINotesRevisionNumber: Int?
    var content: Data?
    var createdAt: Date

    init(meetingID: UUID, dto: DocumentRevisionDTO) throws {
        self.id = dto.id
        self.meetingID = meetingID
        self.kindRaw = dto.kind.rawValue
        self.revisionNumber = dto.revisionNumber
        self.originRaw = dto.origin.rawValue
        self.restoredFromRevisionNumber = dto.restoredFromRevisionNumber
        self.reviewedAt = dto.reviewedAt
        self.sourceTranscriptRevisionID = dto.source.transcriptRevisionId
        self.sourceTranscriptRevisionNumber = dto.source.transcriptRevisionNumber
        self.sourceManualNotesRevisionID = dto.source.manualNotesRevisionId
        self.sourceManualNotesRevisionNumber = dto.source.manualNotesRevisionNumber
        self.sourceAINotesRevisionID = dto.source.aiNotesRevisionId
        self.sourceAINotesRevisionNumber = dto.source.aiNotesRevisionNumber
        self.content = try dto.content?.encoded()
        self.createdAt = dto.createdAt
    }

    var kind: DocumentKind { DocumentKind(rawValue: kindRaw) ?? .manualNotes }
    var origin: RevisionOrigin { RevisionOrigin(rawValue: originRaw) ?? .userEdit }

    func decodedContent() throws -> DocumentContentValue? {
        try content.map { try DocumentContentValue.decode(kind: kind, from: $0) }
    }
}

@Model
final class ProcessingJob {
    @Attribute(.unique) var id: UUID
    var meetingID: UUID
    var typeRaw: String
    var trigger: String
    var statusRaw: String
    var attempts: Int
    var maxAttempts: Int
    var errorCategory: String?
    var updatedAt: Date

    init(dto: JobDTO) {
        self.id = dto.id
        self.meetingID = dto.meetingId
        self.typeRaw = dto.type.rawValue
        self.trigger = dto.trigger
        self.statusRaw = dto.status.rawValue
        self.attempts = dto.attempts
        self.maxAttempts = dto.maxAttempts
        self.errorCategory = dto.errorCategory
        self.updatedAt = dto.finishedAt ?? dto.startedAt ?? dto.createdAt
    }

    var type: JobType { JobType(rawValue: typeRaw) ?? .transcribe }
    var status: JobStatus { JobStatus(rawValue: statusRaw) ?? .queued }

    func apply(_ dto: JobDTO) {
        statusRaw = dto.status.rawValue
        attempts = dto.attempts
        maxAttempts = dto.maxAttempts
        errorCategory = dto.errorCategory
        updatedAt = dto.finishedAt ?? dto.startedAt ?? dto.createdAt
    }
}

// MARK: - Local-only

@Model
final class LocalRecording {
    @Attribute(.unique) var meetingID: UUID
    var ownerUserID: UUID
    var stateRaw: String
    var startedAt: Date
    var activeDurationMs: Int = 0
    @Relationship(deleteRule: .cascade, inverse: \RecordingSegment.recording) var segments: [RecordingSegment] = []
    var mergedFileName: String?

    init(meetingID: UUID, ownerUserID: UUID, state: LocalRecordingState = .recording, startedAt: Date = .now) {
        self.meetingID = meetingID
        self.ownerUserID = ownerUserID
        self.stateRaw = state.rawValue
        self.startedAt = startedAt
    }

    var state: LocalRecordingState {
        get { LocalRecordingState(rawValue: stateRaw) ?? .stopped }
        set { stateRaw = newValue.rawValue }
    }

    var orderedSegments: [RecordingSegment] { segments.sorted { $0.index < $1.index } }
}

@Model
final class RecordingSegment {
    var index: Int
    var fileName: String
    var durationMs: Int
    var bytes: Int
    var closedAt: Date
    var recording: LocalRecording?

    init(index: Int, fileName: String, durationMs: Int, bytes: Int, closedAt: Date = .now) {
        self.index = index
        self.fileName = fileName
        self.durationMs = durationMs
        self.bytes = bytes
        self.closedAt = closedAt
    }

    static func fileName(index: Int) -> String { String(format: "seg-%04d.m4a", index) }
}

@Model
final class PendingUpload {
    @Attribute(.unique) var meetingID: UUID
    var ownerUserID: UUID
    var filePath: String
    var sizeBytes: Int
    var sha256: String
    var contentType: String
    var durationMs: Int
    var uploadID: UUID?
    var receivedBytes: Int = 0
    var stateRaw: String = PendingUploadState.waiting.rawValue
    var rejectCode: String?
    var attempts: Int = 0
    var nextAttemptAt: Date = Date.now

    init(meetingID: UUID, ownerUserID: UUID, filePath: String, sizeBytes: Int, sha256: String, contentType: String, durationMs: Int) {
        self.meetingID = meetingID
        self.ownerUserID = ownerUserID
        self.filePath = filePath
        self.sizeBytes = sizeBytes
        self.sha256 = sha256
        self.contentType = contentType
        self.durationMs = durationMs
    }

    var state: PendingUploadState {
        get { PendingUploadState(rawValue: stateRaw) ?? .waiting }
        set { stateRaw = newValue.rawValue }
    }
}

@Model
final class PendingIntent {
    var meetingID: UUID
    var typeRaw: String
    @Attribute(.unique) var idempotencyKey: UUID
    var body: Data
    var keySent: Bool = false
    var lastErrorCode: String?
    var quotaBlockedLogged: Bool = false
    var createdAt: Date = Date.now

    init(meetingID: UUID, type: IntentType, idempotencyKey: UUID = UUID(), body: Data) {
        self.meetingID = meetingID
        self.typeRaw = type.rawValue
        self.idempotencyKey = idempotencyKey
        self.body = body
    }

    var type: IntentType { IntentType(rawValue: typeRaw) ?? .transcribe }

    static func transcribe(meetingID: UUID, language: TranscriptionLanguage) -> PendingIntent {
        PendingIntent(meetingID: meetingID, type: .transcribe, body: APICoding.encode(TranscriptionRequest(language: language)))
    }
}

@Model
final class PendingDocumentEdit {
    var meetingID: UUID
    var kindRaw: String
    var content: Data?
    var baseRevision: Int
    var expectedRevision: Int
    var inFlightMutationID: UUID?
    var inFlightContent: Data?
    var inFlightExpected: Int?
    var inFlightBase: Int?
    var conflictCurrentRevision: Int?
    var failedCode: String?
    var updatedAt: Date

    init(meetingID: UUID, kind: DocumentKind, content: Data, baseRevision: Int) {
        self.meetingID = meetingID
        self.kindRaw = kind.rawValue
        self.content = content
        self.baseRevision = baseRevision
        self.expectedRevision = baseRevision
        self.updatedAt = .now
    }

    var kind: DocumentKind { DocumentKind(rawValue: kindRaw) ?? .manualNotes }
    var hasPendingWork: Bool { content != nil || inFlightMutationID != nil }
}

@Model
final class PendingMutation {
    var meetingID: UUID?
    var kindRaw: String
    var payload: Data
    var targetID: UUID?
    var createdAt: Date
    var attempts: Int = 0
    var failedCode: String?

    init(meetingID: UUID?, kind: MutationKind, targetID: UUID? = nil, payload: Data = Data()) {
        self.meetingID = meetingID
        self.kindRaw = kind.rawValue
        self.targetID = targetID
        self.payload = payload
        self.createdAt = .now
    }

    var kind: MutationKind { MutationKind(rawValue: kindRaw) ?? .meetingPatch }
}

@Model
final class PendingEvent {
    @Attribute(.unique) var eventID: UUID
    var name: String
    var occurredAt: Date
    var meetingID: UUID?

    init(name: AnalyticsEvent, meetingID: UUID?, occurredAt: Date = .now, eventID: UUID = UUID()) {
        self.eventID = eventID
        self.name = name.rawValue
        self.occurredAt = occurredAt
        self.meetingID = meetingID
    }

    var dto: EventDTO? {
        AnalyticsEvent(rawValue: name).map { EventDTO(eventId: eventID, name: $0, occurredAt: occurredAt, meetingId: meetingID) }
    }
}
