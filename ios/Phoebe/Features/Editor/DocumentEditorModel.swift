import Foundation
import SwiftData
import SwiftUI

struct RevisionMeta: Identifiable, Equatable, Sendable {
    let id: UUID
    let number: Int
    let origin: RevisionOrigin
    let restoredFrom: Int?
    let reviewedAt: Date?
    let createdAt: Date
    let transcriptRevisionID: UUID?
    let transcriptNumber: Int?
    let aiNotesNumber: Int?

    init(_ dto: DocumentRevisionDTO) {
        id = dto.id
        number = dto.revisionNumber
        origin = dto.origin
        restoredFrom = dto.restoredFromRevisionNumber
        reviewedAt = dto.reviewedAt
        createdAt = dto.createdAt
        transcriptRevisionID = dto.source.transcriptRevisionId
        transcriptNumber = dto.source.transcriptRevisionNumber
        aiNotesNumber = dto.source.aiNotesRevisionNumber
    }

    init(_ rev: DocumentRevision) {
        id = rev.id
        number = rev.revisionNumber
        origin = rev.origin
        restoredFrom = rev.restoredFromRevisionNumber
        reviewedAt = rev.reviewedAt
        createdAt = rev.createdAt
        transcriptRevisionID = rev.sourceTranscriptRevisionID
        transcriptNumber = rev.sourceTranscriptRevisionNumber
        aiNotesNumber = rev.sourceAINotesRevisionNumber
    }

    var originText: LocalizedStringResource {
        switch origin {
        case .aiGenerated: L("version.origin.ai", table: .editor)
        case .userEdit: L("version.origin.edit", table: .editor)
        case .restore: L("version.origin.restore", table: .editor, Int64(restoredFrom ?? 0))
        }
    }
}

struct EditorDocState: Equatable, Sendable {
    var currentRevisionNumber: Int
    var currentRevisionID: UUID
    var reviewStatus: ReviewStatus
    var isStale: Bool
    var staleReasons: [String]

    init(_ d: Document) {
        currentRevisionNumber = d.currentRevisionNumber
        currentRevisionID = d.currentRevisionID
        reviewStatus = d.reviewStatus
        isStale = d.isStale
        staleReasons = d.staleReasons
    }
}

struct SourcePlayRequest: Identifiable, Equatable {
    let id = UUID()
    let meetingID: UUID
    let startMs: Int
    let endMs: Int
    var approximate = false
    let text: String?
}

@MainActor @Observable
final class DocumentEditorModel {
    enum Phase: Equatable { case loading, ready, failed }

    struct Viewed: Equatable {
        let meta: RevisionMeta
        let document: EditorDocument
    }

    let meetingID: UUID
    let kind: DocumentKind

    private(set) var phase: Phase = .loading
    private(set) var draft: EditorDraft?
    private(set) var loadedRevision = 0
    private(set) var loadedMeta: RevisionMeta?
    private(set) var pending: PendingEditSnapshot?
    private(set) var otherPending: [DocumentKind: PendingEditSnapshot] = [:]
    private(set) var doc: EditorDocState?
    private(set) var isDebouncing = false
    private(set) var isFlushing = false
    private(set) var flushFailed = false
    private(set) var isReviewing = false
    private(set) var isRestoring = false
    private(set) var isRequestingRegen = false
    private(set) var regenLimitedUntil: Date?
    private(set) var reviewedCount = 0
    private(set) var hasLocalAudio = false
    private(set) var versions: [RevisionMeta] = []
    private(set) var versionsLoading = false
    private(set) var versionsFromDevice = false
    private var locallyReverted = false
    private var reviewOverride: ReviewStatus?

    var viewing: Viewed?
    var showConflict = false
    var showVersions = false
    var showExport = false
    var showRegenConfirm = false
    var sectionPendingDelete: MoMSection?
    var playing: SourcePlayRequest?
    var showsReviewFootnote = false
    var offlineBannerDismissed = false
    var failedBannerDismissed = false

    @ObservationIgnored private let app: AppModel
    @ObservationIgnored private var autosaveTask: Task<Void, Never>?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private var retryBackoff = Backoff.upload
    @ObservationIgnored private var lastPersisted: Data?
    @ObservationIgnored private var flushAgain = false
    @ObservationIgnored private var pendingRestore: (number: Int, expected: Int, id: UUID)?
    @ObservationIgnored private var segmentCache: [String: TranscriptSegment]?

    static let autosaveDelay: Duration = .seconds(1)

    init(meetingID: UUID, kind: DocumentKind, app: AppModel) {
        self.meetingID = meetingID
        self.kind = kind
        self.app = app
    }

    private var context: ModelContext? { app.modelContext }
    var document: EditorDocument? { draft?.document }
    var isReadOnly: Bool { viewing != nil }

    var saveState: SaveState {
        SaveStateResolver.resolve(debouncing: isDebouncing, pending: pending, online: app.isOnline, flushing: isFlushing, flushFailed: flushFailed)
    }

    var reviewStatus: ReviewStatus {
        if locallyReverted || pending?.hasPendingWork == true { return .draft }
        return reviewOverride ?? doc?.reviewStatus ?? .draft
    }

    var conflictRevision: Int? { pending?.conflictCurrentRevision }

    var newerRevision: Int? {
        guard phase == .ready, let doc, doc.currentRevisionNumber > loadedRevision, !isFlushing, pending?.inFlight != true else { return nil }
        return doc.currentRevisionNumber
    }

    // MARK: Loading

    func load(preferNetwork: Bool = false) async {
        phase = .loading
        viewing = nil
        hasLocalAudio = EditorAudioFile.url(meetingID: meetingID) != nil
        if !preferNetwork, let row = fetchPendingRow(), let data = row.content ?? row.inFlightContent, let document = decode(data) {
            adopt(document, revision: row.baseRevision, meta: fetchRevision(number: row.baseRevision).map(RevisionMeta.init))
            return
        }
        if !preferNetwork || !app.isOnline, let d = fetchDocument(), let rev = fetchRevision(id: d.currentRevisionID),
           let data = rev.content, let document = decode(data) {
            adopt(document, revision: rev.revisionNumber, meta: RevisionMeta(rev))
            return
        }
        guard app.isOnline else {
            if fetchDocument() == nil { adoptTemplate() } else { phase = .failed }
            return
        }
        do {
            let response = try await app.api.send(Endpoints.document(meetingID: meetingID, kind: kind))
            store(response.document, revision: response.revision)
            guard let revision = response.revision, let content = revision.content, let document = EditorDocument(content) else {
                phase = .failed
                return
            }
            adopt(document, revision: revision.revisionNumber, meta: RevisionMeta(revision))
        } catch APIError.notFound {
            adoptTemplate()
        } catch {
            phase = .failed
        }
    }

    private func adopt(_ document: EditorDocument, revision: Int, meta: RevisionMeta?) {
        draft = EditorDraft(document)
        loadedRevision = revision
        loadedMeta = meta
        lastPersisted = try? document.wireData()
        locallyReverted = false
        reviewOverride = nil
        segmentCache = nil
        phase = .ready
    }

    private func adoptTemplate() {
        let id = meetingID
        let meeting = try? context?.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
        let language = (try? context?.fetch(FetchDescriptor<UserProfile>()).first)?.language ?? app.user?.language ?? "id"
        let value: DocumentContentValue = kind == .mom
            ? .mom(.emptyTemplate(
                language: language,
                title: meeting?.title ?? "",
                startedAt: meeting?.startedAt,
                timezone: meeting?.timezone ?? TimeZone.current.identifier,
                participants: meeting?.participants ?? []))
            : .aiNotes(AINotesContent(language: language, sections: []))
        guard let document = EditorDocument(value) else { return }
        adopt(document, revision: 0, meta: nil)
    }

    private func decode(_ data: Data) -> EditorDocument? {
        (try? DocumentContentValue.decode(kind: kind, from: data)).flatMap(EditorDocument.init)
    }

    // MARK: Observed store state (fed by the view's @Query)

    func observe(pending new: PendingEditSnapshot?, others: [DocumentKind: PendingEditSnapshot], doc newDoc: EditorDocState?) {
        let old = pending
        let oldDoc = doc
        pending = new
        otherPending = others
        doc = newDoc
        if let new, new.hasPendingWork, new.baseRevision > loadedRevision { loadedRevision = new.baseRevision }
        // Our edit reached the server and the outbox row is gone: the current revision is ours.
        if old?.hasPendingWork == true, new?.hasPendingWork != true, new?.isBlocked != true, let newDoc, phase == .ready {
            loadedRevision = newDoc.currentRevisionNumber
            loadedMeta = fetchRevision(id: newDoc.currentRevisionID).map(RevisionMeta.init) ?? loadedMeta
            flushFailed = false
        }
        if oldDoc?.currentRevisionNumber != newDoc?.currentRevisionNumber { reviewOverride = nil }
        if new?.conflictCurrentRevision != nil, old?.conflictCurrentRevision == nil { showConflict = true }
    }

    func connectivityChanged(online: Bool) {
        if online, pending?.hasPendingWork == true { flush() }
    }

    // MARK: Editing

    private func edit<T>(_ body: (inout EditorDraft) -> T) -> T? {
        guard viewing == nil, var d = draft else { return nil }
        let before = d.document
        let result = body(&d)
        draft = d
        if d.document != before { contentChanged() }
        return result
    }

    @discardableResult
    func setText(_ target: EditTarget, _ value: String) -> EditTarget? {
        edit { $0.setText(target, value) } ?? nil
    }

    func setStartedAt(_ date: Date?) { edit { $0.setStartedAt(date) } }
    func setDeadline(section: String, item: String, day: String?) { edit { $0.setDeadline(section: section, item: item, day: day) } }
    @discardableResult func addParticipant() -> EditTarget? { edit { $0.addParticipant() } }
    @discardableResult func addAgenda(after id: String? = nil) -> EditTarget? { edit { $0.addAgenda(after: id) } }
    @discardableResult func addItem(section: String, after id: String? = nil) -> EditTarget? { edit { $0.addItem(section: section, after: id) } ?? nil }
    @discardableResult func addPoint(section: String, topic: String) -> EditTarget? { edit { $0.addPoint(section: section, topic: topic) } }
    @discardableResult func delete(_ target: EditTarget) -> EditTarget? { edit { $0.delete(target) } ?? nil }
    func moveItem(section: String, item: String, by offset: Int) { edit { $0.moveItem(section: section, item: item, by: offset) } }
    func setStyle(section: String, _ style: ListStyle) { edit { $0.setStyle(section: section, style) } }
    func moveSection(_ id: String, by offset: Int) { edit { $0.moveSection(id, by: offset) } }
    func undo() { edit { $0.undo() } }
    func redo() { edit { $0.redo() } }

    @discardableResult
    func addSection(_ type: SectionType) -> EditTarget? {
        let language = document?.language
        let heading = type == .custom ? LS("mom.section.new", table: .editor, localization: language) : type.defaultHeading(language: language ?? "id")
        return edit { $0.addSection(type, heading: heading) } ?? nil
    }

    func deleteSection(_ id: String) {
        edit { $0.deleteSection(id) }
        app.toasts.show(L("section.deleted", table: .editor), actionTitle: L("editor.undo", table: .editor)) { [weak self] in self?.undo() }
    }

    private func contentChanged() {
        if reviewStatus == .reviewed { app.toasts.show(L("review.reverted", table: .editor)) }
        locallyReverted = true
        reviewOverride = nil
        showsReviewFootnote = false
        scheduleAutosave()
    }

    // MARK: Autosave → DocumentEditOutbox (sync.saveDraft), then push

    private func scheduleAutosave() {
        isDebouncing = true
        autosaveTask?.cancel()
        autosaveTask = Task { [weak self] in
            try? await Task.sleep(for: Self.autosaveDelay)
            guard !Task.isCancelled else { return }
            self?.persistNow()
        }
    }

    @discardableResult
    func persistNow() -> Bool {
        autosaveTask?.cancel()
        autosaveTask = nil
        isDebouncing = false
        guard let document, let data = try? document.wireData(), data != lastPersisted else { return false }
        lastPersisted = data
        app.sync.saveDraft(meetingID: meetingID, kind: kind, wireContent: data, loadedRevision: loadedRevision)
        flush()
        return true
    }

    func flush() {
        guard app.isOnline else { return }
        guard !isFlushing else {
            flushAgain = true
            return
        }
        isFlushing = true
        retryTask?.cancel()
        Task {
            let ok = await app.sync.flushEdits(meetingID: meetingID, kinds: [kind])
            isFlushing = false
            if ok {
                flushFailed = false
                retryBackoff.reset()
            } else if pending?.hasPendingWork == true, pending?.isBlocked != true, app.isOnline {
                flushFailed = true
                scheduleRetry()
            }
            if flushAgain {
                flushAgain = false
                flush()
            }
        }
    }

    private func scheduleRetry() {
        let delay = retryBackoff.nextDelay()
        retryTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            self?.flush()
        }
    }

    func retryNow() {
        retryBackoff.reset()
        flush()
    }

    func leave() {
        let wrote = persistNow()
        retryTask?.cancel()
        if wrote || pending?.hasPendingWork == true {
            app.toasts.show(L("editor.leave.saved", table: .editor))
        }
    }

    // MARK: Conflict

    func resolveConflict(_ resolution: ConflictResolution) async {
        switch resolution {
        case .keepMine:
            persistNow()
            await app.sync.resolveDocumentConflict(meetingID: meetingID, kind: kind, .keepMine)
            flush()
        case .useLatest:
            autosaveTask?.cancel()
            isDebouncing = false
            phase = .loading
            await app.sync.resolveDocumentConflict(meetingID: meetingID, kind: kind, .useLatest)
            await load(preferNetwork: true)
        }
    }

    // MARK: Review (online; pending edits pushed first so you review what you saw)

    func setReview(_ status: ReviewStatus) async {
        guard app.isOnline, !isReviewing else { return }
        persistNow()
        isReviewing = true
        defer { isReviewing = false }
        do {
            try await app.sync.setReview(meetingID: meetingID, kind: kind, status: status)
            locallyReverted = false
            reviewOverride = status
            if status == .reviewed {
                reviewedCount += 1
                showsReviewFootnote = true
            }
            let statusText = LS(status == .reviewed ? "review.reviewed" : "review.draft", table: .editor)
            AccessibilityNotification.Announcement(LS("review.status.a11y", table: .editor, statusText)).post()
        } catch {
            app.toasts.show(L("review.failed", table: .editor))
        }
    }

    // MARK: Regenerate

    func regenerateBlockReason(usage: UsageDTO?, now: Date = .now) -> LocalizedStringResource? {
        guard app.isOnline else { return L("doc.regen.offline", table: .editor) }
        let sources: [DocumentKind] = kind == .mom ? [.manualNotes, .aiNotes, .mom] : [.manualNotes, .aiNotes]
        let blocked = isDebouncing || sources.contains { k in
            let p = k == kind ? pending : otherPending[k]
            return p?.hasPendingWork == true || p?.isBlocked == true
        }
        if blocked { return L("doc.regen.syncFirst", table: .editor) }
        let limitedUntil = [usage?.aiGenerations.nextSlotAt, regenLimitedUntil].compactMap { $0 }.max()
        if let limitedUntil, limitedUntil > now {
            return L("doc.regen.limited", table: .editor, limitedUntil.formatted(.relative(presentation: .named)))
        }
        return nil
    }

    func regenerate() async {
        persistNow()
        isRequestingRegen = true
        defer { isRequestingRegen = false }
        let kinds: [DocumentKind] = kind == .mom ? [.manualNotes, .aiNotes, .mom] : [.manualNotes, .aiNotes]
        guard await app.sync.flushEdits(meetingID: meetingID, kinds: kinds) else {
            app.toasts.show(L("doc.regen.syncFirst", table: .editor))
            return
        }
        let type: IntentType = kind == .mom ? .generateMom : .generateNotes
        let body = kind == .mom
            ? APICoding.encode(GenerateMoMRequest(aiNotesRevisionId: nil, language: nil))
            : APICoding.encode(GenerateNotesRequest(language: nil))
        do {
            try await app.sync.requestGeneration(meetingID: meetingID, type: type, body: body)
            failedBannerDismissed = false
            app.poller.reset()
            app.poller.start(.meeting(meetingID))
        } catch APIError.aiGenerationLimit(let next) {
            let usage = await app.refreshUsage()
            regenLimitedUntil = next ?? usage?.aiGenerations.nextSlotAt
        } catch APIError.jobInProgress {
            app.poller.start(.meeting(meetingID))
        } catch {
            app.toasts.show(L("doc.regen.failed", table: .editor))
        }
    }

    func openNewer() async {
        guard let newer = newerRevision else { return }
        if pending?.hasPendingWork == true {
            await openVersion(number: newer)
        } else {
            await load()
        }
    }

    // MARK: Versions

    func loadVersions() async {
        versionsLoading = true
        defer { versionsLoading = false }
        if app.isOnline {
            do {
                var items: [DocumentRevisionDTO] = []
                var cursor: String?
                repeat {
                    let page = try await app.api.send(Endpoints.documentRevisions(meetingID: meetingID, kind: kind, limit: 200, cursor: cursor))
                    items += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                versions = items.map(RevisionMeta.init)
                versionsFromDevice = false
                return
            } catch {}
        }
        versions = localRevisions().map(RevisionMeta.init)
        versionsFromDevice = true
    }

    func openVersion(_ meta: RevisionMeta) async {
        if meta.number == loadedRevision, pending?.hasPendingWork != true, doc?.currentRevisionNumber == meta.number {
            viewing = nil
            return
        }
        await openVersion(number: meta.number)
    }

    func openVersion(number: Int) async {
        if let local = fetchRevision(number: number), let data = local.content, let document = decode(data) {
            viewing = Viewed(meta: RevisionMeta(local), document: document)
            return
        }
        guard app.isOnline else {
            app.toasts.show(L("version.loadFailed", table: .editor))
            return
        }
        do {
            let revision = try await app.api.send(Endpoints.documentRevision(meetingID: meetingID, kind: kind, number: number)).revision
            upsertRevision(revision)
            try? context?.save()
            guard let content = revision.content, let document = EditorDocument(content) else { return }
            viewing = Viewed(meta: RevisionMeta(revision), document: document)
        } catch {
            app.toasts.show(L("version.loadFailed", table: .editor))
        }
    }

    func restore(_ number: Int) async {
        guard app.isOnline, !isRestoring else { return }
        persistNow()
        isRestoring = true
        defer { isRestoring = false }
        if pending?.hasPendingWork == true || isFlushing {
            guard await app.sync.flushEdits(meetingID: meetingID, kinds: [kind]) else {
                app.toasts.show(L("version.restore.failed", table: .editor))
                return
            }
        }
        guard let current = fetchDocument()?.currentRevisionNumber else { return }
        // A retry after an unknown outcome must resend the identical body with the same mutation id.
        let mutation = pendingRestore.flatMap { $0.number == number && $0.expected == current ? $0.id : nil } ?? UUID()
        pendingRestore = (number, current, mutation)
        do {
            let response = try await app.api.send(Endpoints.restoreDocument(
                meetingID: meetingID, kind: kind,
                RestoreRequest(revisionNumber: number, expectedRevision: current, mutationId: mutation)))
            pendingRestore = nil
            store(response.document, revision: response.revision)
            showVersions = false
            if let revision = response.revision, let content = revision.content, let document = EditorDocument(content) {
                adopt(document, revision: revision.revisionNumber, meta: RevisionMeta(revision))
                viewing = nil
            } else {
                await load(preferNetwork: true)
            }
            app.toasts.show(L("version.restored", table: .editor, Int64(response.document.currentRevisionNumber)))
        } catch let error as APIError where error.isAmbiguous {
            app.toasts.show(L("version.restore.failed", table: .editor))
        } catch {
            pendingRestore = nil
            app.toasts.show(L("version.restore.failed", table: .editor))
            await app.sync.refreshMeeting(meetingID)
        }
    }

    // MARK: Source references

    func isVerified(_ ref: SourceRef) -> Bool {
        guard case .transcript(let segmentID, _, _, _) = ref else { return true }
        let map = segments()
        return map.isEmpty || map[segmentID] != nil
    }

    func play(_ ref: SourceRef) {
        guard case .transcript(let segmentID, let startMs, let endMs, let approximate) = ref else { return }
        playing = SourcePlayRequest(meetingID: meetingID, startMs: startMs, endMs: endMs, approximate: approximate, text: segments()[segmentID]?.text)
    }

    private func segments() -> [String: TranscriptSegment] {
        if let segmentCache { return segmentCache }
        let id = meetingID
        let revisions = (try? context?.fetch(FetchDescriptor<TranscriptRevision>(
            predicate: #Predicate { $0.meetingID == id },
            sortBy: [SortDescriptor(\.revisionNumber, order: .reverse)]))) ?? []
        let pinned = loadedMeta?.transcriptRevisionID
        let chosen = revisions.first { $0.id == pinned } ?? revisions.first
        let map = ((try? chosen?.segments()) ?? []).reduce(into: [String: TranscriptSegment]()) { $0[$1.id] = $1 }
        segmentCache = map
        return map
    }

    // MARK: Store helpers

    private func fetchPendingRow() -> PendingDocumentEdit? {
        let id = meetingID, raw = kind.rawValue
        return try? context?.fetch(FetchDescriptor<PendingDocumentEdit>(predicate: #Predicate { $0.meetingID == id && $0.kindRaw == raw })).first
    }

    private func fetchDocument() -> Document? {
        let id = meetingID, raw = kind.rawValue
        return try? context?.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.meetingID == id && $0.kindRaw == raw })).first
    }

    private func fetchRevision(id: UUID) -> DocumentRevision? {
        try? context?.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.id == id })).first
    }

    private func fetchRevision(number: Int) -> DocumentRevision? {
        let id = meetingID, raw = kind.rawValue
        return try? context?.fetch(FetchDescriptor<DocumentRevision>(
            predicate: #Predicate { $0.meetingID == id && $0.kindRaw == raw && $0.revisionNumber == number })).first
    }

    private func localRevisions() -> [DocumentRevision] {
        let id = meetingID, raw = kind.rawValue
        return (try? context?.fetch(FetchDescriptor<DocumentRevision>(
            predicate: #Predicate { $0.meetingID == id && $0.kindRaw == raw },
            sortBy: [SortDescriptor(\.revisionNumber, order: .reverse)]))) ?? []
    }

    private func store(_ summary: DocumentSummaryDTO, revision: DocumentRevisionDTO?) {
        guard let context else { return }
        let docID = summary.id
        if let existing = try? context.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.id == docID })).first {
            if summary.currentRevisionNumber >= existing.currentRevisionNumber { existing.apply(summary) }
        } else {
            let d = Document(id: summary.id, meetingID: meetingID, kind: kind, currentRevisionNumber: summary.currentRevisionNumber,
                             currentRevisionID: summary.currentRevisionId, reviewStatus: summary.reviewStatus, updatedAt: summary.updatedAt)
            d.apply(summary)
            context.insert(d)
        }
        if let revision { upsertRevision(revision) }
        try? context.save()
    }

    private func upsertRevision(_ dto: DocumentRevisionDTO) {
        if let existing = fetchRevision(id: dto.id) {
            if existing.content == nil, let content = dto.content { existing.content = try? content.encoded() }
            existing.reviewedAt = dto.reviewedAt
            return
        }
        if let rev = try? DocumentRevision(meetingID: meetingID, dto: dto) { context?.insert(rev) }
    }
}

enum EditorAudioFile {
    static func url(meetingID: UUID) -> URL? {
        guard let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else { return nil }
        for name in [meetingID.uuidString, meetingID.lower] {
            let dir = support.appending(path: "recordings/\(name)", directoryHint: .isDirectory)
            let merged = dir.appending(path: "audio.m4a")
            if FileManager.default.fileExists(atPath: merged.path) { return merged }
            let files = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
            if let imported = files.first(where: { $0.lastPathComponent.hasPrefix("import.") }) { return imported }
        }
        return cachedURL(meetingID: meetingID)
    }

    static var cacheDirectory: URL? {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first?.appending(path: "audio", directoryHint: .isDirectory)
    }

    static func cachedURL(meetingID: UUID) -> URL? {
        guard let dir = cacheDirectory else { return nil }
        let files = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        return files.first { $0.deletingPathExtension().lastPathComponent == meetingID.lower }
    }
}
