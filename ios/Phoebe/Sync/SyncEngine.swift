import Foundation
import SwiftData

// ios.md §10 / api §6: pull (never inferring deletion from absence) and per-meeting outbox push.
@MainActor @Observable
final class SyncEngine: SyncEngineAPI {
    private(set) var isPulling = false
    private(set) var lastPullFailed = false

    @ObservationIgnored private let env: ServiceEnvironment
    @ObservationIgnored private let sleep: @Sendable (TimeInterval) async -> Void
    @ObservationIgnored private let automatic: Bool
    @ObservationIgnored private var queues: [UUID: Task<OutboxResult, Never>] = [:]
    @ObservationIgnored private var retryBackoff = Backoff.upload
    @ObservationIgnored private var retryTask: Task<Void, Never>?

    private static let globalQueue = UUID(uuid: (0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0))

    private var store: SyncStore { SyncStore(context: env.context) }
    private var mirror: MeetingMirror { MeetingMirror(store: store) }
    private var edits: DocumentEditOutbox { DocumentEditOutbox(env: env) }
    private var outbox: MutationOutbox { MutationOutbox(env: env) }

    convenience init(env: ServiceEnvironment) {
        self.init(env: env, sleep: { try? await Task.sleep(for: .seconds($0)) }, automatic: true)
    }

    // `automatic: false` (tests) disables the push triggered by enqueue/saveDraft and the retry timer.
    init(env: ServiceEnvironment, sleep: @escaping @Sendable (TimeInterval) async -> Void, automatic: Bool) {
        self.env = env
        self.sleep = sleep
        self.automatic = automatic
    }

    // MARK: Pull

    func fullPull() async {
        guard !isPulling else { return }
        isPulling = true
        defer { isPulling = false }
        await pushAll()
        await SyncRemote.refreshFoldersAndTags(env: env)

        var seen = Set<UUID>()
        var changed: [UUID] = []
        var cursor: String?
        do {
            repeat {
                let page = try await env.api.send(Endpoints.listMeetings(limit: 200, cursor: cursor))
                for item in page.items {
                    seen.insert(item.id)
                    if upsertTracking(item) { changed.append(item.id) }
                }
                cursor = page.nextCursor
            } while cursor != nil
            store.save()
        } catch {
            store.save()
            lastPullFailed = true
            return
        }
        lastPullFailed = false

        for meeting in store.meetings() where meeting.isServerCreated && !seen.contains(meeting.id) && meeting.localPhase != .orphaned {
            await store.confirmMissing(meeting.id, api: env.api, uploads: env.app.uploads)
        }
        for id in changed where store.meeting(id).map({ !$0.activeJobTypes.isEmpty }) == true {
            await refreshMeeting(id)
        }
        store.meetings().filter(\.hasTranscript).forEach { applyLocalAudioPolicy($0.id) }
    }

    // ios.md §8: the local file goes only once a transcript exists and the user opted in; never right after upload.
    private func applyLocalAudioPolicy(_ id: UUID) {
        let userID = env.userID
        guard let meeting = store.meeting(id), meeting.hasTranscript,
              let profile = try? env.context.fetch(FetchDescriptor<UserProfile>(predicate: #Predicate { $0.id == userID })).first,
              profile.deleteLocalAudioAfterTranscript,
              let upload = store.pendingUpload(id), upload.state == .verified else { return }
        SyncFiles.remove(SyncFiles.resolve(upload.filePath))
        SyncFiles.recordingsDirs(id).forEach(SyncFiles.remove)
        env.context.delete(upload)
        store.localRecording(id).map(env.context.delete)
        store.save()
    }

    @discardableResult
    func pollTick() async -> Bool {
        if let page = try? await env.api.send(Endpoints.listMeetings()) {
            var changed: [UUID] = []
            for item in page.items where upsertTracking(item) { changed.append(item.id) }
            store.save()
            for id in changed where store.meeting(id).map({ !$0.activeJobTypes.isEmpty }) == true {
                await refreshMeeting(id)
            }
        }
        return store.meetings().contains(where: Self.isActive)
    }

    static func isActive(_ meeting: Meeting) -> Bool {
        !meeting.activeJobTypes.isEmpty || [.queued, .uploading, .verifying].contains(meeting.localPhase)
    }

    private func upsertTracking(_ item: MeetingListItemDTO) -> Bool {
        let previous = store.meeting(item.id)?.updatedAt
        mirror.upsert(item)
        return previous != item.updatedAt
    }

    func refreshMeeting(_ id: UUID) async {
        if let meeting = store.meeting(id), !meeting.isServerCreated { return }
        do {
            let dto = try await env.api.send(Endpoints.meeting(id)).meeting
            mirror.upsert(dto)
            store.save()
            applyLocalAudioPolicy(id)
            if let pointer = dto.transcript,
               !store.transcriptRevisions(id).contains(where: { $0.revisionNumber == pointer.currentRevisionNumber }),
               let transcript = try? await env.api.send(Endpoints.transcript(meetingID: id)).revision {
                mirror.upsert(transcript, meetingID: id)
                store.save()
            }
            for kind in DocumentEditOutbox.order {
                guard let summary = dto.documents[kind],
                      store.documentRevision(summary.currentRevisionId)?.content == nil else { continue }
                await SyncRemote.refreshDocument(id, kind: kind, env: env)
            }
        } catch APIError.notFound {
            await store.confirmMissing(id, api: env.api, uploads: env.app.uploads)
        } catch {
            SyncLog.logger.info("refresh meeting failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    // MARK: Push

    func pushAll() async {
        guard env.app.isOnline else { return }
        var needsRetry = false
        let global = await serialized(Self.globalQueue) { await self.pushGlobal() }
        if global == .stop { return }
        needsRetry = global == .retry
        for id in meetingsWithWork() {
            let result = await serialized(id) { await self.pushMeeting(id) }
            if result == .stop { return }
            if result == .retry { needsRetry = true }
        }
        await env.app.events.flush()
        if needsRetry { scheduleRetry() } else { retryBackoff.reset() }
    }

    private func meetingsWithWork() -> [UUID] {
        var ids: [UUID] = []
        func add(_ id: UUID?) { if let id, !ids.contains(id) { ids.append(id) } }
        store.allMutations().forEach { add($0.meetingID) }
        store.allEdits().forEach { add($0.meetingID) }
        store.allIntents().forEach { add($0.meetingID) }
        store.pendingUploads().filter { $0.state != .verified }.forEach { add($0.meetingID) }
        return ids
    }

    private func pushGlobal() async -> OutboxResult {
        for mutation in store.mutations(nil) where mutation.failedCode == nil {
            switch await outbox.push(mutation) {
            case .retry: return .retry
            case .stop: return .stop
            default: continue
            }
        }
        return .done
    }

    private func pushMeeting(_ id: UUID) async -> OutboxResult {
        let meeting = store.meeting(id)
        if meeting?.localPhase == .orphaned { return .halt }

        if let meeting, !meeting.isServerCreated {
            do {
                guard try await SyncRemote.ensureMeeting(id, env: env) else { return .halt }
            } catch let error as APIError {
                let result = OutboxResult.classify(error)
                if result == .blocked {
                    store.mutations(id).first { $0.kind == .meetingCreate }?.failedCode = error.code
                    store.save()
                    return .halt
                }
                return result
            } catch {
                return .halt
            }
        }

        for mutation in store.mutations(id) where mutation.failedCode == nil {
            switch await outbox.push(mutation) {
            case .done, .blocked: continue
            case .missing:
                await store.confirmMissing(id, api: env.api, uploads: env.app.uploads)
                return .missing
            case let other: return other
            }
        }
        guard store.meeting(id) != nil else { return .done }

        switch await edits.push(id) {
        case .stop: return .stop
        case .missing:
            await store.confirmMissing(id, api: env.api, uploads: env.app.uploads)
            return .missing
        case .retry: return .retry
        default: break
        }

        var result = OutboxResult.done
        for intent in store.intents(id) where intent.lastErrorCode == nil && intentGateOpen(intent) {
            switch await IntentSender.send(intent, env: env) {
            case .retryLater(let error?) where error.isAmbiguous || error == .uploadBusy:
                result = .retry
            case .retryLater(let error?) where error == .offline || error == .unauthenticated:
                return .stop
            case .missing:
                await store.confirmMissing(id, api: env.api, uploads: env.app.uploads)
                return .missing
            default:
                break
            }
        }

        if let upload = store.pendingUpload(id), ![.verified, .failed, .rejected].contains(upload.state) {
            await env.app.uploads.kick(meetingID: id)
        }
        return result
    }

    // ios.md §10 gates: transcribe waits for this device's upload to verify; generate waits for
    // the source document edits (manual notes, and AI notes for MoM) to reach the server.
    private func intentGateOpen(_ intent: PendingIntent) -> Bool {
        switch intent.type {
        case .transcribe:
            return store.pendingUpload(intent.meetingID).map { $0.state == .verified } ?? true
        case .generateNotes:
            return edits.isClean(intent.meetingID, kinds: [.manualNotes])
        case .generateMom:
            return edits.isClean(intent.meetingID, kinds: [.manualNotes, .aiNotes])
        }
    }

    private func serialized(_ key: UUID, _ work: @escaping @MainActor () async -> OutboxResult) async -> OutboxResult {
        let previous = queues[key]
        let task = Task { @MainActor in
            _ = await previous?.value
            return await work()
        }
        queues[key] = task
        let result = await task.value
        if queues[key] == task { queues[key] = nil }
        return result
    }

    private func scheduleRetry() {
        guard automatic, retryTask == nil else { return }
        let delay = retryBackoff.nextDelay()
        retryTask = Task { [weak self, sleep] in
            await sleep(delay)
            self?.retryTask = nil
            await self?.pushAll()
        }
    }

    private func pushSoon() {
        guard automatic else { return }
        Task { await pushAll() }
    }

    // MARK: Mutations

    func enqueue(_ mutation: OutboxMutation) {
        outbox.enqueue(mutation)
        pushSoon()
    }

    func saveDraft(meetingID: UUID, kind: DocumentKind, wireContent: Data, loadedRevision: Int) {
        edits.saveDraft(meetingID: meetingID, kind: kind, wireContent: wireContent, loadedRevision: loadedRevision)
        pushSoon()
    }

    func flushEdits(meetingID: UUID, kinds: [DocumentKind]) async -> Bool {
        if env.app.isOnline {
            _ = await serialized(meetingID) { await self.pushMeeting(meetingID) }
        }
        return edits.isClean(meetingID, kinds: kinds)
    }

    func resolveDocumentConflict(meetingID: UUID, kind: DocumentKind, _ resolution: ConflictResolution) async {
        await edits.resolveConflict(meetingID: meetingID, kind: kind, resolution)
        if resolution == .keepMine, env.app.isOnline {
            _ = await serialized(meetingID) { await self.pushMeeting(meetingID) }
        }
    }

    func resolveMetadataConflict(meetingID: UUID, _ resolution: ConflictResolution) async {
        await outbox.resolveMetadataConflict(meetingID, resolution)
        if resolution == .keepMine, env.app.isOnline {
            _ = await serialized(meetingID) { await self.pushMeeting(meetingID) }
        }
    }

    // Review what you saw: pending edits for the document must reach the server first.
    func setReview(meetingID: UUID, kind: DocumentKind, status: ReviewStatus) async throws {
        guard await flushEdits(meetingID: meetingID, kinds: [kind]) else { throw SyncError.pendingEdits }
        guard let document = store.document(meetingID, kind) else { throw APIError.notFound }
        do {
            let response = try await env.api.send(Endpoints.setReview(meetingID: meetingID, kind: kind, status: status, expectedRevision: document.currentRevisionNumber))
            mirror.upsert(response, meetingID: meetingID)
            if let revision = store.documentRevision(response.document.currentRevisionId) {
                revision.reviewedAt = status == .reviewed ? (revision.reviewedAt ?? .now) : nil
            }
            store.save()
            if kind == .mom, status == .reviewed { env.app.events.log(.momReviewed, meetingID: meetingID) }
        } catch APIError.revisionConflict(let current, let currentID) {
            await SyncRemote.refreshDocument(meetingID, kind: kind, env: env)
            throw APIError.revisionConflict(currentRevisionNumber: current, currentRevisionId: currentID)
        }
    }

    // A new user intent: one key, persisted before the first send and reused on every retry.
    // Throws only definitive rejections; offline / ambiguous / gated intents stay queued.
    func requestGeneration(meetingID: UUID, type: IntentType, body: Data) async throws {
        let existing = store.intents(meetingID).first { $0.type == type }
        let intent: PendingIntent
        if let existing, existing.body == body {
            existing.lastErrorCode = nil
            intent = existing
        } else {
            existing.map(env.context.delete)
            intent = PendingIntent(meetingID: meetingID, type: type, body: body)
            env.context.insert(intent)
        }
        store.save()

        guard env.app.isOnline, store.meeting(meetingID)?.isServerCreated == true, intentGateOpen(intent) else {
            pushSoon()
            return
        }
        switch await IntentSender.send(intent, env: env) {
        case .done:
            env.app.poller.reset()
        case .rejected(let error):
            throw error
        case .waiting(let code):
            throw code == "entitlement_inactive" ? APIError.entitlementInactive(periodEnd: nil) : APIError.quotaExceeded(requiredSeconds: nil, remainingSeconds: nil)
        case .missing:
            await store.confirmMissing(meetingID, api: env.api, uploads: env.app.uploads)
            throw APIError.notFound
        case .retryLater(let error?) where !error.isAmbiguous && error != .offline && error != .uploadBusy:
            if error == .audioNotReady { return }
            if error == .idempotencyKeyReused { pushSoon(); return }
            throw error
        case .retryLater, .skipped:
            scheduleRetry()
        }
    }

    func search(_ query: String) async throws -> [MeetingListItemDTO] {
        guard env.app.isOnline else { throw APIError.offline }
        return try await env.api.send(Endpoints.listMeetings(q: query, limit: 50)).items
    }

    // MARK: Orphans (ios.md §10)

    func keepOrphanAsNewMeeting(_ id: UUID) async -> UUID? {
        guard let old = store.meeting(id), old.localPhase == .orphaned else { return nil }
        env.app.uploads.cancel(meetingID: id)
        let newID = UUID()

        if let dir = SyncFiles.existingRecordingsDir(id) {
            let name = dir.lastPathComponent == id.uuidString ? newID.uuidString : newID.lower
            try? FileManager.default.moveItem(at: dir, to: SyncFiles.recordingsRoot.appending(path: name, directoryHint: .isDirectory))
        }

        let meeting = Meeting(id: newID, source: old.source, title: old.title, startedAt: old.startedAt, timezone: old.timezone, language: old.transcriptionLanguage)
        meeting.participants = old.participants
        meeting.folder = old.folder
        meeting.tags = old.tags
        meeting.durationMs = old.durationMs
        meeting.refreshSearchText()
        env.context.insert(meeting)

        store.localRecording(id)?.meetingID = newID

        if let upload = store.pendingUpload(id) {
            let path = upload.filePath
                .replacingOccurrences(of: id.lower, with: newID.lower)
                .replacingOccurrences(of: id.uuidString, with: newID.uuidString)
            if FileManager.default.fileExists(atPath: SyncFiles.resolve(path).path) {
                if let uploadID = upload.uploadID { SyncFiles.remove(SyncFiles.stagingDir(uploadID)) }
                upload.meetingID = newID
                upload.filePath = path
                upload.uploadID = nil
                upload.receivedBytes = 0
                upload.attempts = 0
                upload.rejectCode = nil
                upload.state = .waiting
                meeting.localPhase = .queued
                if !store.intents(id).contains(where: { $0.type == .transcribe }) {
                    env.context.insert(PendingIntent.transcribe(meetingID: newID, language: meeting.transcriptionLanguage))
                }
            } else {
                env.context.delete(upload)
            }
        }

        for intent in store.intents(id) {
            guard intent.type == .transcribe else {
                env.context.delete(intent)
                continue
            }
            intent.meetingID = newID
            intent.idempotencyKey = UUID()
            intent.keySent = false
            intent.lastErrorCode = nil
            intent.quotaBlockedLogged = false
        }

        let notesContent = store.edit(id, .manualNotes).flatMap { $0.content ?? $0.inFlightContent }
            ?? store.documentRevisions(id, .manualNotes).max { $0.revisionNumber < $1.revisionNumber }?.content
        store.edits(id).forEach(env.context.delete)
        if let notesContent {
            env.context.insert(PendingDocumentEdit(meetingID: newID, kind: .manualNotes, content: notesContent, baseRevision: 0))
        }

        let oldRevisions = store.documentRevisions(id)
        SyncFiles.removeExports(revisionIDs: oldRevisions.map(\.id))
        oldRevisions.forEach(env.context.delete)
        store.mutations(id).forEach(env.context.delete)
        store.transcriptRevisions(id).forEach(env.context.delete)
        store.jobs(id).forEach(env.context.delete)
        for kind in DocumentEditOutbox.order { store.document(id, kind).map(env.context.delete) }
        env.context.delete(old)
        env.context.insert(PendingMutation(meetingID: newID, kind: .meetingCreate))
        store.save()
        pushSoon()
        return newID
    }

    func discardOrphan(_ id: UUID) {
        store.purgeMeeting(id, uploads: env.app.uploads)
    }
}
