import Foundation
import SwiftData
import SwiftUI

enum DetailTab: String, CaseIterable {
    case transcript, notes, mom
}

enum CorrectionOutcome: Equatable {
    case saved
    case conflict(latestRevision: Int)
    case failed(key: String)
}

@MainActor @Observable
final class MeetingDetailModel {
    let meetingID: UUID

    var generating: Set<IntentType> = []
    var actionErrors: [IntentType: String] = [:]
    var retryAfterUntil: Date?
    var uploadRetrying = false
    var deleting = false
    var versions: [DocumentKind: [DocumentRevisionDTO]] = [:]
    var versionsLoading: Set<DocumentKind> = []
    var dismissedStale: Set<UUID> = []
    var momSourceRevisionID: UUID?
    var showOriginal = false
    @ObservationIgnored var leaveToastShown = false

    var showMetadata = false
    var showFolder = false
    var showTags = false
    var showExport = false
    var showVersions: DocumentKind?
    var confirmDeleteMeeting = false
    var confirmDeleteAudio = false
    var audioAlertKey: String?
    var correctionTarget: TranscriptSegment?

    private(set) var manualDraft: [NoteItem] = []
    private(set) var manualDebouncing = false
    private(set) var manualLoadedRevision = -1
    @ObservationIgnored private var manualDocRevision = 0
    @ObservationIgnored private var manualSaveTask: Task<Void, Never>?
    @ObservationIgnored private var fetching: Set<String> = []

    init(meetingID: UUID) {
        self.meetingID = meetingID
    }

    // MARK: Content fetch (fills gaps the sync engine has not stored yet)

    func ensureContent(app: AppModel, meeting: Meeting?, transcripts: [TranscriptRevision], documents: [Document], revisions: [DocumentRevision]) async {
        guard app.isOnline, let context = app.modelContext, let meeting else { return }
        let current = transcripts.max { $0.revisionNumber < $1.revisionNumber }
        if meeting.hasTranscript && current == nil {
            await fetchOnce("transcript") {
                let dto = try await app.api.send(Endpoints.transcript(meetingID: self.meetingID)).revision
                self.store(dto, in: context)
            }
        }
        if let current, current.kind == .correction, !transcripts.contains(where: { $0.revisionNumber == 1 }) {
            await fetchOnce("transcript.1") {
                let dto = try await app.api.send(Endpoints.transcriptRevision(meetingID: self.meetingID, number: 1)).revision
                self.store(dto, in: context)
            }
        }
        for document in documents {
            let kind = document.kind
            let hasCurrent = revisions.contains { $0.kindRaw == document.kindRaw && $0.revisionNumber == document.currentRevisionNumber && $0.content != nil }
            guard !hasCurrent else { continue }
            await fetchOnce("doc.\(kind.rawValue).\(document.currentRevisionNumber)") {
                let response = try await app.api.send(Endpoints.document(meetingID: self.meetingID, kind: kind))
                self.store(response, in: context)
            }
        }
    }

    private func fetchOnce(_ key: String, _ work: () async throws -> Void) async {
        guard !fetching.contains(key) else { return }
        fetching.insert(key)
        defer { fetching.remove(key) }
        try? await work()
    }

    func store(_ dto: TranscriptRevisionDTO, in context: ModelContext) {
        guard let segments = dto.segments else { return }
        let id = dto.id
        if (try? context.fetch(FetchDescriptor<TranscriptRevision>(predicate: #Predicate { $0.id == id })).first) != nil { return }
        let data = (try? APICoding.encoder().encode(segments)) ?? Data("[]".utf8)
        context.insert(TranscriptRevision(id: dto.id, meetingID: meetingID, revisionNumber: dto.revisionNumber, kind: dto.kind, language: dto.language, createdAt: dto.createdAt, segmentsJSON: data))
        if let meeting = RecordingModel.meeting(meetingID, in: context) { meeting.hasTranscript = true }
        try? context.save()
    }

    func store(_ response: DocumentResponse, in context: ModelContext) {
        let summary = response.document
        let docID = summary.id
        if let existing = try? context.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.id == docID })).first {
            if summary.currentRevisionNumber >= existing.currentRevisionNumber { existing.apply(summary) }
        } else {
            let doc = Document(id: summary.id, meetingID: meetingID, kind: summary.kind, currentRevisionNumber: summary.currentRevisionNumber, currentRevisionID: summary.currentRevisionId, reviewStatus: summary.reviewStatus, updatedAt: summary.updatedAt)
            doc.isStale = summary.isStale
            doc.staleReasons = summary.staleReasons
            context.insert(doc)
        }
        if let revision = response.revision {
            store(revision, in: context)
        }
        try? context.save()
    }

    func store(_ revision: DocumentRevisionDTO, in context: ModelContext) {
        let revID = revision.id
        if let existing = try? context.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.id == revID })).first {
            if existing.content == nil, let content = revision.content { existing.content = try? content.encoded() }
        } else if let row = try? DocumentRevision(meetingID: meetingID, dto: revision) {
            context.insert(row)
        }
        try? context.save()
    }

    // MARK: Generation & retries

    func generate(_ type: IntentType, body: Data, flushKinds: [DocumentKind], pendingKinds: Set<DocumentKind>, app: AppModel) async {
        guard !generating.contains(type) else { return }
        generating.insert(type)
        actionErrors[type] = nil
        defer { generating.remove(type) }

        if flushKinds.contains(.manualNotes) { saveManualNow(app: app) }
        let toFlush = flushKinds.filter { pendingKinds.contains($0) || ($0 == .manualNotes && manualHasUnsaved) }
        if !toFlush.isEmpty {
            guard await app.sync.flushEdits(meetingID: meetingID, kinds: toFlush) else {
                actionErrors[type] = "doc.regen.syncFirst"
                return
            }
        }
        do {
            try await app.sync.requestGeneration(meetingID: meetingID, type: type, body: body)
        } catch let error as APIError {
            switch error {
            case .jobInProgress, .alreadyTranscribed:
                break
            case .rateLimited(let seconds):
                retryAfterUntil = Date.now.addingTimeInterval(TimeInterval(seconds ?? 60))
                actionErrors[type] = "doc.regen.limited"
                return
            default:
                actionErrors[type] = Self.errorKey(error)
                return
            }
        } catch {
            actionErrors[type] = "detail.action.failed"
            return
        }
        app.poller.start(.meeting(meetingID))
        app.poller.reset()
        await app.sync.refreshMeeting(meetingID)
    }

    static func errorKey(_ error: APIError) -> String {
        switch error {
        case .offline: "detail.ai.offline"
        case .aiNotesNotReady: "mom.needsNotes"
        case .transcriptNotReady: "detail.notes.needsTranscript"
        case .quotaExceeded: "detail.quota.waiting"
        case .entitlementInactive: "detail.entitlement.expired"
        case .audioUnavailable: "detail.player.expired"
        case .audioNotReady: "detail.upload.waiting"
        case .revisionConflict: "doc.regen.syncFirst"
        default: "detail.action.failed"
        }
    }

    func notesBody() -> Data { APICoding.encode(GenerateNotesRequest(language: nil)) }

    func momBody() -> Data { APICoding.encode(GenerateMoMRequest(aiNotesRevisionId: momSourceRevisionID, language: nil)) }

    func transcribeBody(language: TranscriptionLanguage) -> Data { APICoding.encode(TranscriptionRequest(language: language)) }

    func retryUpload(app: AppModel) async {
        uploadRetrying = true
        defer { uploadRetrying = false }
        await app.uploads.retry(meetingID: meetingID)
        app.poller.reset()
    }

    // MARK: Transcript correction (online only, no outbox)

    func submitCorrection(segmentID: String, text: String, expectedRevision: Int, mutationID: UUID, app: AppModel) async -> CorrectionOutcome {
        guard app.isOnline else { return .failed(key: "transcript.edit.offline") }
        let body = TranscriptCorrectionRequest(expectedRevision: expectedRevision, mutationId: mutationID, edits: [SegmentEdit(segmentId: segmentID, text: text)])
        var attempt = 0
        while true {
            do {
                let dto = try await app.api.send(Endpoints.correctTranscript(meetingID: meetingID, body)).revision
                if let context = app.modelContext { store(dto, in: context) }
                app.poller.reset()
                await app.sync.refreshMeeting(meetingID)
                return .saved
            } catch let error as APIError {
                if error.isAmbiguous && attempt < 2 {
                    attempt += 1
                    try? await Task.sleep(for: .seconds(attempt * 2))
                    continue
                }
                switch error {
                case .revisionConflict(let current, _):
                    if let latest = try? await app.api.send(Endpoints.transcript(meetingID: meetingID)).revision, let context = app.modelContext {
                        store(latest, in: context)
                    }
                    return .conflict(latestRevision: current)
                case .offline: return .failed(key: "transcript.edit.offline")
                case .invalidContent, .validationFailed: return .failed(key: "transcript.edit.invalid")
                default: return .failed(key: "detail.action.failed")
                }
            } catch {
                return .failed(key: "detail.action.failed")
            }
        }
    }

    // MARK: Versions & restore

    func loadVersions(_ kind: DocumentKind, app: AppModel) async {
        guard app.isOnline, !versionsLoading.contains(kind) else { return }
        versionsLoading.insert(kind)
        defer { versionsLoading.remove(kind) }
        var all: [DocumentRevisionDTO] = []
        var cursor: String?
        repeat {
            guard let page = try? await app.api.send(Endpoints.documentRevisions(meetingID: meetingID, kind: kind, limit: 50, cursor: cursor)) else { return }
            all += page.items
            cursor = page.nextCursor
        } while cursor != nil
        versions[kind] = all.sorted { $0.revisionNumber > $1.revisionNumber }
    }

    func fetchRevision(_ kind: DocumentKind, number: Int, app: AppModel) async -> DocumentContentValue? {
        if let context = app.modelContext {
            let raw = kind.rawValue
            let local = try? context.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw && $0.revisionNumber == number })).first
            if let value = try? local?.decodedContent() { return value }
        }
        guard let dto = try? await app.api.send(Endpoints.documentRevision(meetingID: meetingID, kind: kind, number: number)).revision else { return nil }
        if let context = app.modelContext { store(dto, in: context) }
        return dto.content
    }

    func restore(_ kind: DocumentKind, number: Int, expectedRevision: Int, mutationID: UUID, app: AppModel) async -> String? {
        guard app.isOnline else { return "detail.ai.offline" }
        let body = RestoreRequest(revisionNumber: number, expectedRevision: expectedRevision, mutationId: mutationID)
        var attempt = 0
        while true {
            do {
                let response = try await app.api.send(Endpoints.restoreDocument(meetingID: meetingID, kind: kind, body))
                if let context = app.modelContext { store(response, in: context) }
                app.toasts.show(L("version.restored", table: .meetingDetail, Int64(response.document.currentRevisionNumber)))
                await loadVersions(kind, app: app)
                return nil
            } catch let error as APIError {
                if error.isAmbiguous && attempt < 2 {
                    attempt += 1
                    try? await Task.sleep(for: .seconds(attempt * 2))
                    continue
                }
                if case .revisionConflict = error {
                    await app.sync.refreshMeeting(meetingID)
                    return "version.restore.conflict"
                }
                return "detail.action.failed"
            } catch {
                return "detail.action.failed"
            }
        }
    }

    // MARK: Manual notes draft

    var manualHasUnsaved: Bool { manualDebouncing }

    func loadManual(edit: PendingDocumentEdit?, revision: DocumentRevision?, documentRevision: Int, force: Bool = false) {
        manualDocRevision = documentRevision
        let firstLoad = manualLoadedRevision < 0
        let serverAdvanced = documentRevision != manualLoadedRevision && !manualDebouncing && edit?.hasPendingWork != true
        guard force || (firstLoad && !manualDebouncing) || serverAdvanced else { return }
        let data = edit?.content ?? edit?.inFlightContent ?? revision?.content
        if let data, case .manualNotes(let content)? = try? DocumentContentValue.decode(kind: .manualNotes, from: data) {
            manualDraft = content.items
        } else {
            manualDraft = []
        }
        // Stay unloaded until the server revision's content is available locally.
        manualLoadedRevision = data != nil || documentRevision == 0 ? documentRevision : -1
    }

    func editPoint(_ id: String, text: String, app: AppModel) -> String? {
        let (items, focus) = NotePointEditing.applyEdit(manualDraft, id: id, text: text)
        guard items != manualDraft else { return nil }
        manualDraft = items
        scheduleManualSave(app: app)
        return focus
    }

    func addPoint() -> String {
        let item = NoteItem.new()
        manualDraft.append(item)
        return item.id
    }

    func deletePoint(_ id: String, app: AppModel) {
        manualDraft.removeAll { $0.id == id }
        scheduleManualSave(app: app)
    }

    func scheduleManualSave(app: AppModel) {
        manualDebouncing = true
        manualSaveTask?.cancel()
        manualSaveTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            self?.saveManualNow(app: app)
        }
    }

    func saveManualNow(app: AppModel) {
        manualSaveTask?.cancel()
        manualSaveTask = nil
        guard manualDebouncing else { return }
        manualDebouncing = false
        guard let data = try? DocumentContentValue.manualNotes(ManualNotesContent(items: manualDraft)).wireData() else { return }
        app.sync.saveDraft(meetingID: meetingID, kind: .manualNotes, wireContent: data, loadedRevision: manualDocRevision)
    }

    // MARK: MoM

    func createManualMoM(meeting: Meeting, app: AppModel) {
        let language = app.user?.language ?? "id"
        let content = MoMContent.emptyTemplate(language: language, title: meeting.title, startedAt: meeting.startedAt, timezone: meeting.timezone, participants: meeting.participants)
        guard let data = try? DocumentContentValue.mom(content).wireData() else { return }
        app.sync.saveDraft(meetingID: meetingID, kind: .mom, wireContent: data, loadedRevision: 0)
        app.path.append(.editor(meetingID: meetingID, kind: .mom))
    }

    // MARK: Deletion

    func requestDeleteAudio(transcribeActive: Bool, uploadUnverified: Bool) {
        if transcribeActive {
            audioAlertKey = "audio.delete.busy"
        } else if uploadUnverified {
            audioAlertKey = "audio.delete.blocked"
        } else {
            confirmDeleteAudio = true
        }
    }

    func deleteAudio(player: PlayerModel, app: AppModel) {
        player.stop()
        PlayerModel.removeCache(meetingID: meetingID)
        app.sync.enqueue(.deleteAudio(meetingID))
    }

    func deleteMeeting(player: PlayerModel, app: AppModel) async {
        deleting = true
        player.stop()
        manualSaveTask?.cancel()
        manualDebouncing = false
        app.poller.stop(.meeting(meetingID))
        app.uploads.cancel(meetingID: meetingID)
        PlayerModel.removeCache(meetingID: meetingID)
        app.sync.enqueue(.deleteMeeting(meetingID))
        try? await Task.sleep(for: .milliseconds(300))
        let id = meetingID
        app.path.removeAll { route in
            switch route {
            case .meeting(let m): m == id
            case .editor(let m, _): m == id
            case .settings: false
            }
        }
        app.toasts.show(L("detail.deleted", table: .meetingDetail))
    }
}
