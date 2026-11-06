import Foundation

// MARK: - Enums (api §2, §4)

enum MeetingSource: String, Codable, Sendable {
    case recording
    case `import`
}

enum PipelineState: String, Codable, Sendable {
    case awaitingAudio = "awaiting_audio"
    case uploading, uploaded, transcribing
    case transcriptionFailed = "transcription_failed"
    case transcriptReady = "transcript_ready"
    case generatingNotes = "generating_notes"
    case notesFailed = "notes_failed"
    case notesReady = "notes_ready"
}

enum JobType: String, Codable, Sendable, CaseIterable {
    case transcribe
    case generateNotes = "generate_notes"
    case generateMom = "generate_mom"
}

enum JobStatus: String, Codable, Sendable {
    case queued, running, succeeded, failed, canceled
    var isActive: Bool { self == .queued || self == .running }
}

enum ReviewStatus: String, Codable, Sendable {
    case draft, reviewed
}

enum RevisionOrigin: String, Codable, Sendable {
    case aiGenerated = "ai_generated"
    case userEdit = "user_edit"
    case restore
}

enum TranscriptKind: String, Codable, Sendable {
    case original, correction
}

// "chunk" start/end are our silence-aligned chunk bounds, not provider timestamps: always shown as approximate.
enum TranscriptTiming: String, Codable, Sendable {
    case segment, chunk
}

enum NotesMode: String, Codable, Sendable {
    case auto, skip
}

enum UploadStatus: String, Codable, Sendable {
    case open, verifying, completed, failed, expired, canceled
}

enum ExportFormat: String, Codable, Sendable, CaseIterable {
    case md, whatsapp, docx, pdf
}

enum EntitlementStatus: String, Codable, Sendable {
    case active, expired, inactive
}

enum TranscriptionLanguage: String, Codable, Sendable, CaseIterable {
    case auto, id, en
}

enum OutputLanguage: String, Codable, Sendable, CaseIterable {
    case id, en
}

enum AnalyticsEvent: String, Codable, Sendable, CaseIterable {
    case onboardingCompleted = "onboarding_completed"
    case recordingStarted = "recording_started"
    case recordingInterrupted = "recording_interrupted"
    case recordingSaved = "recording_saved"
    case documentSaved = "document_saved"
    case momReviewed = "mom_reviewed"
    case exportCompleted = "export_completed"
    case shareSheetOpened = "share_sheet_opened"
    case quotaBlocked = "quota_blocked"
}

// MARK: - Response objects

struct ServerConfig: Codable, Equatable, Sendable {
    var maxAudioDurationMs: Int
    var maxUploadBytes: Int
    var uploadChunkMaxBytes: Int
    var supportedContentTypes: [String]
    var audioRetentionDays: Int
    var languages: [String]
    var fetchedAt: Date?

    static let defaultMaxAudioDurationMs = 7_200_000
}

struct HealthDTO: Decodable, Sendable { let status: String }

struct UserDTO: Codable, Equatable, Sendable {
    let id: UUID
    var displayName: String
    var language: String
    var timezone: String
    let createdAt: Date
}

struct TokenPairDTO: Codable, Equatable, Sendable {
    let accessToken: String
    let accessTokenExpiresAt: Date
    let refreshToken: String
    let refreshTokenExpiresAt: Date
    let sessionExpiresAt: Date
}

struct AuthResponseDTO: Decodable, Sendable {
    let user: UserDTO
    let tokens: TokenPairDTO

    private enum CodingKeys: String, CodingKey { case user }

    init(from decoder: Decoder) throws {
        user = try decoder.container(keyedBy: CodingKeys.self).decode(UserDTO.self, forKey: .user)
        tokens = try TokenPairDTO(from: decoder)
    }
}

struct UserEnvelope: Decodable, Sendable { let user: UserDTO }
struct PurgeResponseDTO: Decodable, Sendable { let purgeScheduledAt: Date }

struct FolderDTO: Codable, Equatable, Sendable {
    let id: UUID
    var name: String
    var meetingCount: Int
    let createdAt: Date
    var updatedAt: Date
}

typealias TagDTO = FolderDTO

struct FolderEnvelope: Decodable, Sendable { let folder: FolderDTO }
struct TagEnvelope: Decodable, Sendable { let tag: TagDTO }

struct ItemsResponse<Item: Decodable & Sendable>: Decodable, Sendable { let items: [Item] }

struct Page<Item: Decodable & Sendable>: Decodable, Sendable {
    let items: [Item]
    let nextCursor: String?
}

struct AudioAssetDTO: Codable, Equatable, Sendable {
    let id: UUID
    let contentType: String
    let sizeBytes: Int
    let durationMs: Int
    let sha256: String
    let available: Bool
    let deletedReason: String?
    let retainUntil: Date?
    let createdAt: Date
}

struct UploadSessionDTO: Codable, Equatable, Sendable {
    let id: UUID
    let meetingId: UUID
    let status: UploadStatus
    let contentType: String
    let sizeBytes: Int
    let receivedBytes: Int
    let sha256: String
    let durationMs: Int
    let chunkMaxBytes: Int
    let failureReason: String?
    let autoTranscription: Bool
    let expiresAt: Date
    let createdAt: Date
}

struct JobResultDTO: Codable, Equatable, Sendable {
    let transcriptRevisionId: UUID?
    let documentRevisionId: UUID?
    let droppedItems: Int?
}

struct JobDTO: Codable, Equatable, Sendable {
    let id: UUID
    let meetingId: UUID
    let type: JobType
    let trigger: String
    let status: JobStatus
    let attempts: Int
    let maxAttempts: Int
    let errorCategory: String?
    let result: JobResultDTO?
    let createdAt: Date
    let startedAt: Date?
    let finishedAt: Date?
}

struct TranscriptPointerDTO: Codable, Equatable, Sendable {
    let currentRevisionId: UUID
    let currentRevisionNumber: Int
}

struct DocumentSummaryDTO: Codable, Equatable, Sendable {
    let id: UUID
    let kind: DocumentKind
    let currentRevisionNumber: Int
    let currentRevisionId: UUID
    let reviewStatus: ReviewStatus
    let isStale: Bool
    let staleReasons: [String]
    let updatedAt: Date
}

struct MeetingDocumentsDTO: Codable, Equatable, Sendable {
    let manualNotes: DocumentSummaryDTO?
    let aiNotes: DocumentSummaryDTO?
    let mom: DocumentSummaryDTO?

    subscript(kind: DocumentKind) -> DocumentSummaryDTO? {
        switch kind {
        case .manualNotes: manualNotes
        case .aiNotes: aiNotes
        case .mom: mom
        }
    }
}

struct MeetingJobsDTO: Codable, Equatable, Sendable {
    let transcribe: JobDTO?
    let generateNotes: JobDTO?
    let generateMom: JobDTO?

    subscript(type: JobType) -> JobDTO? {
        switch type {
        case .transcribe: transcribe
        case .generateNotes: generateNotes
        case .generateMom: generateMom
        }
    }
}

struct MeetingDTO: Codable, Equatable, Sendable {
    let id: UUID
    let source: MeetingSource
    let title: String
    let startedAt: Date?
    let timezone: String
    let language: TranscriptionLanguage
    let participants: [String]
    let metadataRevision: Int
    let folderId: UUID?
    let tagIds: [UUID]
    let pipelineState: PipelineState
    let createdAt: Date
    let updatedAt: Date
    let audio: AudioAssetDTO?
    let upload: UploadSessionDTO?
    let transcript: TranscriptPointerDTO?
    let documents: MeetingDocumentsDTO
    let jobs: MeetingJobsDTO
}

struct MeetingEnvelope: Decodable, Sendable { let meeting: MeetingDTO }

struct MeetingListItemDTO: Codable, Equatable, Sendable {
    let id: UUID
    let source: MeetingSource
    let title: String
    let startedAt: Date?
    let timezone: String
    let folderId: UUID?
    let tagIds: [UUID]
    let pipelineState: PipelineState
    let durationMs: Int?
    let audioAvailable: Bool
    let momReviewStatus: ReviewStatus?
    let activeJobTypes: [JobType]
    let failedStage: String?
    let transcriptionErrorCategory: String?
    let createdAt: Date
    let updatedAt: Date
}

struct UploadEnvelope: Decodable, Sendable { let upload: UploadSessionDTO }

struct TranscriptionBlockedDTO: Decodable, Equatable, Sendable {
    let code: String
    let nextSlotAt: Date?
}

struct UploadResultDTO: Decodable, Sendable {
    let upload: UploadSessionDTO
    let audio: AudioAssetDTO?
    let job: JobDTO?
    let transcriptionBlocked: TranscriptionBlockedDTO?
}

struct JobEnvelope: Decodable, Sendable { let job: JobDTO }

struct TranscriptSegment: Codable, Equatable, Identifiable, Sendable {
    let id: String
    let startMs: Int
    let endMs: Int
    var text: String
    var uncertain: Bool
    var uncertainReason: String?
    // Copied from the revision so locally stored segments (segmentsJSON) keep it; absent → chunk (never imply precision).
    var timing: TranscriptTiming

    init(id: String, startMs: Int, endMs: Int, text: String, uncertain: Bool = false, uncertainReason: String? = nil, timing: TranscriptTiming = .segment) {
        self.id = id
        self.startMs = startMs
        self.endMs = endMs
        self.text = text
        self.uncertain = uncertain
        self.uncertainReason = uncertainReason
        self.timing = timing
    }

    var isApproximate: Bool { timing == .chunk }

    enum CodingKeys: String, CodingKey { case id, startMs, endMs, text, uncertain, uncertainReason, timing }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        startMs = try c.decode(Int.self, forKey: .startMs)
        endMs = try c.decode(Int.self, forKey: .endMs)
        text = try c.decode(String.self, forKey: .text)
        uncertain = try c.decodeIfPresent(Bool.self, forKey: .uncertain) ?? false
        uncertainReason = try c.decodeIfPresent(String.self, forKey: .uncertainReason)
        timing = try c.decodeIfPresent(TranscriptTiming.self, forKey: .timing) ?? .chunk
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(startMs, forKey: .startMs)
        try c.encode(endMs, forKey: .endMs)
        try c.encode(text, forKey: .text)
        try c.encode(uncertain, forKey: .uncertain)
        try c.encodeNullable(uncertainReason, forKey: .uncertainReason)
        try c.encode(timing, forKey: .timing)
    }
}

struct TranscriptRevisionDTO: Decodable, Equatable, Sendable {
    let id: UUID
    let revisionNumber: Int
    let kind: TranscriptKind
    let language: String
    let timing: TranscriptTiming
    let createdAt: Date
    let segments: [TranscriptSegment]?

    enum CodingKeys: String, CodingKey { case id, revisionNumber, kind, language, timing, createdAt, segments }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        revisionNumber = try c.decode(Int.self, forKey: .revisionNumber)
        kind = try c.decode(TranscriptKind.self, forKey: .kind)
        language = try c.decode(String.self, forKey: .language)
        let timing = try c.decode(TranscriptTiming.self, forKey: .timing)
        self.timing = timing
        createdAt = try c.decode(Date.self, forKey: .createdAt)
        segments = try c.decodeIfPresent([TranscriptSegment].self, forKey: .segments)?.map {
            var segment = $0
            segment.timing = timing
            return segment
        }
    }
}

struct TranscriptRevisionEnvelope: Decodable, Sendable { let revision: TranscriptRevisionDTO }

struct DocumentSourceDTO: Codable, Equatable, Sendable {
    let transcriptRevisionId: UUID?
    let transcriptRevisionNumber: Int?
    let manualNotesRevisionId: UUID?
    let manualNotesRevisionNumber: Int?
    let aiNotesRevisionId: UUID?
    let aiNotesRevisionNumber: Int?
}

struct DocumentRevisionDTO: Decodable, Equatable, Sendable {
    let id: UUID
    let documentId: UUID
    let kind: DocumentKind
    let revisionNumber: Int
    let origin: RevisionOrigin
    let restoredFromRevisionNumber: Int?
    let reviewedAt: Date?
    let createdAt: Date
    let source: DocumentSourceDTO
    let content: DocumentContentValue?

    enum CodingKeys: String, CodingKey {
        case id, documentId, kind, revisionNumber, origin, restoredFromRevisionNumber, reviewedAt, createdAt, source, content
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        documentId = try c.decode(UUID.self, forKey: .documentId)
        kind = try c.decode(DocumentKind.self, forKey: .kind)
        revisionNumber = try c.decode(Int.self, forKey: .revisionNumber)
        origin = try c.decode(RevisionOrigin.self, forKey: .origin)
        restoredFromRevisionNumber = try c.decodeIfPresent(Int.self, forKey: .restoredFromRevisionNumber)
        reviewedAt = try c.decodeIfPresent(Date.self, forKey: .reviewedAt)
        createdAt = try c.decode(Date.self, forKey: .createdAt)
        source = try c.decode(DocumentSourceDTO.self, forKey: .source)
        if c.contains(.content), try !c.decodeNil(forKey: .content) {
            content = try DocumentContentValue.decode(kind: kind, from: try c.superDecoder(forKey: .content))
        } else {
            content = nil
        }
    }
}

struct DocumentResponse: Decodable, Sendable {
    let document: DocumentSummaryDTO
    let revision: DocumentRevisionDTO?
}

struct DocumentRevisionEnvelope: Decodable, Sendable { let revision: DocumentRevisionDTO }

struct ExportDTO: Codable, Equatable, Sendable {
    let id: UUID
    let documentRevisionId: UUID
    let kind: DocumentKind
    let format: ExportFormat
    let filename: String
    let contentType: String
    let sizeBytes: Int
    let isDraft: Bool
    let createdAt: Date
}

struct ExportResponse: Decodable, Sendable {
    let export: ExportDTO
    let downloadUrl: URL
    let downloadExpiresAt: Date
    let text: String?
}

struct SignedURLResponse: Decodable, Sendable {
    let url: URL
    let expiresAt: Date
}

struct EntitlementDTO: Codable, Equatable, Sendable {
    let plan: String
    let status: EntitlementStatus
    let periodStart: Date?
    let periodEnd: Date?
    let quotaSeconds: Int
}

struct AIGenerationsDTO: Codable, Equatable, Sendable {
    let limit: Int
    let windowHours: Int
    let used: Int
    let reserved: Int
    let remaining: Int
    let nextSlotAt: Date?
}

struct UsageDTO: Codable, Equatable, Sendable {
    let entitlement: EntitlementDTO
    let consumedSeconds: Int
    let reservedSeconds: Int
    let remainingSeconds: Int
    let aiGenerations: AIGenerationsDTO
}

struct EventsAcceptedDTO: Decodable, Sendable { let accepted: Int }

// MARK: - Request bodies (nullable fields always encoded, ids lowercase)

struct AppleAuthRequest: Encodable, Sendable {
    let identityToken: String
    let nonce: String
    let displayName: String?
    let authorizationCode: String?

    enum CodingKeys: String, CodingKey { case identityToken, nonce, displayName, authorizationCode }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(identityToken, forKey: .identityToken)
        try c.encode(nonce, forKey: .nonce)
        try c.encodeNullable(displayName, forKey: .displayName)
        try c.encodeNullable(authorizationCode, forKey: .authorizationCode)
    }
}

struct DevAuthRequest: Encodable, Sendable { let subject: String }
struct RefreshRequest: Encodable, Sendable { let refreshToken: String }

struct UpdateMeRequest: Encodable, Sendable {
    var displayName: String?
    var language: OutputLanguage?
    var timezone: String?
}

struct CreateMeetingRequest: Encodable, Sendable {
    let id: UUID
    let source: MeetingSource
    let title: String
    let startedAt: Date?
    let timezone: String
    let language: TranscriptionLanguage
    let participants: [String]
    let folderId: UUID?
    let tagIds: [UUID]

    enum CodingKeys: String, CodingKey { case id, source, title, startedAt, timezone, language, participants, folderId, tagIds }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id.lower, forKey: .id)
        try c.encode(source, forKey: .source)
        try c.encode(title, forKey: .title)
        try c.encodeNullable(startedAt, forKey: .startedAt)
        try c.encode(timezone, forKey: .timezone)
        try c.encode(language, forKey: .language)
        try c.encode(participants, forKey: .participants)
        try c.encodeNullable(folderId?.lower, forKey: .folderId)
        try c.encode(tagIds.map(\.lower), forKey: .tagIds)
    }
}

struct CreateNamedRequest: Encodable, Sendable {
    let id: UUID
    let name: String

    enum CodingKeys: String, CodingKey { case id, name }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id.lower, forKey: .id)
        try c.encode(name, forKey: .name)
    }
}

struct RenameRequest: Encodable, Sendable { let name: String }

struct TranscriptionStart: Encodable, Sendable {
    let idempotencyKey: UUID
    let language: TranscriptionLanguage
    var notes: NotesMode = .auto

    enum CodingKeys: String, CodingKey { case idempotencyKey, language, notes }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(idempotencyKey.lower, forKey: .idempotencyKey)
        try c.encode(language, forKey: .language)
        try c.encode(notes, forKey: .notes)
    }
}

struct CreateUploadRequest: Encodable, Sendable {
    let sizeBytes: Int
    let sha256: String
    let contentType: String
    let durationMs: Int
    let transcription: TranscriptionStart?

    enum CodingKeys: String, CodingKey { case sizeBytes, sha256, contentType, durationMs, transcription }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(sizeBytes, forKey: .sizeBytes)
        try c.encode(sha256, forKey: .sha256)
        try c.encode(contentType, forKey: .contentType)
        try c.encode(durationMs, forKey: .durationMs)
        try c.encodeNullable(transcription, forKey: .transcription)
    }
}

struct TranscriptionRequest: Codable, Equatable, Sendable {
    let language: TranscriptionLanguage
    var notes: NotesMode = .auto

    init(language: TranscriptionLanguage, notes: NotesMode = .auto) {
        self.language = language
        self.notes = notes
    }

    // Intent bodies persisted before `notes` existed decode as auto (the server default).
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        language = try c.decode(TranscriptionLanguage.self, forKey: .language)
        notes = try c.decodeIfPresent(NotesMode.self, forKey: .notes) ?? .auto
    }
}

struct SegmentEdit: Encodable, Sendable {
    let segmentId: String
    let text: String
}

struct TranscriptCorrectionRequest: Encodable, Sendable {
    let expectedRevision: Int
    let mutationId: UUID
    let edits: [SegmentEdit]
    var resolvedUncertainSegmentIds: [String] = []

    enum CodingKeys: String, CodingKey { case expectedRevision, mutationId, edits, resolvedUncertainSegmentIds }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(expectedRevision, forKey: .expectedRevision)
        try c.encode(mutationId.lower, forKey: .mutationId)
        try c.encode(edits, forKey: .edits)
        try c.encode(resolvedUncertainSegmentIds, forKey: .resolvedUncertainSegmentIds)
    }
}

struct RestoreRequest: Encodable, Sendable {
    let revisionNumber: Int
    let expectedRevision: Int
    let mutationId: UUID

    enum CodingKeys: String, CodingKey { case revisionNumber, expectedRevision, mutationId }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(revisionNumber, forKey: .revisionNumber)
        try c.encode(expectedRevision, forKey: .expectedRevision)
        try c.encode(mutationId.lower, forKey: .mutationId)
    }
}

struct ReviewRequest: Encodable, Sendable {
    let status: ReviewStatus
    let expectedRevision: Int
}

struct GenerateNotesRequest: Codable, Equatable, Sendable {
    let language: OutputLanguage?

    enum CodingKeys: String, CodingKey { case language }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeNullable(language, forKey: .language)
    }
}

struct GenerateMoMRequest: Codable, Equatable, Sendable {
    let aiNotesRevisionId: UUID?
    let language: OutputLanguage?

    enum CodingKeys: String, CodingKey { case aiNotesRevisionId, language }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeNullable(aiNotesRevisionId?.lower, forKey: .aiNotesRevisionId)
        try c.encodeNullable(language, forKey: .language)
    }
}

struct ExportRequest: Encodable, Sendable {
    let documentRevisionId: UUID
    let format: ExportFormat

    enum CodingKeys: String, CodingKey { case documentRevisionId, format }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(documentRevisionId.lower, forKey: .documentRevisionId)
        try c.encode(format, forKey: .format)
    }
}

struct EventDTO: Encodable, Sendable {
    let eventId: UUID
    let name: AnalyticsEvent
    let occurredAt: Date
    let meetingId: UUID?

    enum CodingKeys: String, CodingKey { case eventId, name, occurredAt, meetingId }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(eventId.lower, forKey: .eventId)
        try c.encode(name, forKey: .name)
        try c.encode(occurredAt, forKey: .occurredAt)
        try c.encodeNullable(meetingId?.lower, forKey: .meetingId)
    }
}

struct EventsRequest: Encodable, Sendable { let events: [EventDTO] }
