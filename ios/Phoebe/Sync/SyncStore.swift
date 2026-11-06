import Foundation
import os
import SwiftData

enum SyncLog {
    static let logger = Logger(subsystem: "com.kolarislabs.phoebe", category: "sync")
}

enum SyncError: Error, Equatable {
    case pendingEdits
    case meetingMissing
}

enum SyncFiles {
    static var recordingsRoot: URL { URL.applicationSupportDirectory.appending(path: "recordings", directoryHint: .isDirectory) }
    static var uploadsRoot: URL { URL.cachesDirectory.appending(path: "uploads", directoryHint: .isDirectory) }

    // The audio engine may name the directory with either UUID casing; accept both.
    static func recordingsDirs(_ meetingID: UUID) -> [URL] {
        Array(Set([meetingID.lower, meetingID.uuidString])).map {
            recordingsRoot.appending(path: $0, directoryHint: .isDirectory)
        }
    }

    static func existingRecordingsDir(_ meetingID: UUID) -> URL? {
        recordingsDirs(meetingID).first { FileManager.default.fileExists(atPath: $0.path) }
    }

    static func stagingDir(_ uploadID: UUID) -> URL {
        uploadsRoot.appending(path: uploadID.lower, directoryHint: .isDirectory)
    }

    static func resolve(_ path: String) -> URL {
        path.hasPrefix("/") ? URL(fileURLWithPath: path) : URL.applicationSupportDirectory.appending(path: path)
    }

    static func hasLocalAudio(_ meetingID: UUID) -> Bool {
        guard let dir = existingRecordingsDir(meetingID),
              let files = try? FileManager.default.contentsOfDirectory(atPath: dir.path) else { return false }
        return !files.isEmpty
    }

    static func remove(_ url: URL) {
        try? FileManager.default.removeItem(at: url)
    }

    // Export cache files are named "<revision_id>-<draft|reviewed>.<format>" (+ ".json" sidecar).
    static func removeExports(revisionIDs: [UUID], directory: URL = ExportCache.standard.directory) {
        guard !revisionIDs.isEmpty, let names = try? FileManager.default.contentsOfDirectory(atPath: directory.path) else { return }
        let prefixes = revisionIDs.map { "\($0.lower)-" }
        for name in names where prefixes.contains(where: name.hasPrefix) {
            remove(directory.appending(path: name))
        }
    }
}

@MainActor
struct SyncStore {
    let context: ModelContext

    func save() {
        do { try context.save() } catch { SyncLog.logger.error("save failed: \(String(describing: error), privacy: .public)") }
    }

    func meeting(_ id: UUID) -> Meeting? {
        try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
    }

    func meetings() -> [Meeting] {
        (try? context.fetch(FetchDescriptor<Meeting>())) ?? []
    }

    func folder(_ id: UUID) -> Folder? {
        try? context.fetch(FetchDescriptor<Folder>(predicate: #Predicate { $0.id == id })).first
    }

    func tag(_ id: UUID) -> Tag? {
        try? context.fetch(FetchDescriptor<Tag>(predicate: #Predicate { $0.id == id })).first
    }

    func pendingUpload(_ meetingID: UUID) -> PendingUpload? {
        try? context.fetch(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.meetingID == meetingID })).first
    }

    func pendingUploads() -> [PendingUpload] {
        (try? context.fetch(FetchDescriptor<PendingUpload>())) ?? []
    }

    func localRecording(_ meetingID: UUID) -> LocalRecording? {
        try? context.fetch(FetchDescriptor<LocalRecording>(predicate: #Predicate { $0.meetingID == meetingID })).first
    }

    func intents(_ meetingID: UUID) -> [PendingIntent] {
        let d = FetchDescriptor<PendingIntent>(predicate: #Predicate { $0.meetingID == meetingID }, sortBy: [SortDescriptor(\.createdAt)])
        return (try? context.fetch(d)) ?? []
    }

    func allIntents() -> [PendingIntent] {
        (try? context.fetch(FetchDescriptor<PendingIntent>(sortBy: [SortDescriptor(\.createdAt)]))) ?? []
    }

    func intent(key: UUID) -> PendingIntent? {
        try? context.fetch(FetchDescriptor<PendingIntent>(predicate: #Predicate { $0.idempotencyKey == key })).first
    }

    func edits(_ meetingID: UUID) -> [PendingDocumentEdit] {
        (try? context.fetch(FetchDescriptor<PendingDocumentEdit>(predicate: #Predicate { $0.meetingID == meetingID }))) ?? []
    }

    func allEdits() -> [PendingDocumentEdit] {
        (try? context.fetch(FetchDescriptor<PendingDocumentEdit>())) ?? []
    }

    func edit(_ meetingID: UUID, _ kind: DocumentKind) -> PendingDocumentEdit? {
        let raw = kind.rawValue
        return try? context.fetch(FetchDescriptor<PendingDocumentEdit>(predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw })).first
    }

    func mutations(_ meetingID: UUID?) -> [PendingMutation] {
        let all = (try? context.fetch(FetchDescriptor<PendingMutation>(sortBy: [SortDescriptor(\.createdAt)]))) ?? []
        return all.filter { $0.meetingID == meetingID }
    }

    func allMutations() -> [PendingMutation] {
        (try? context.fetch(FetchDescriptor<PendingMutation>(sortBy: [SortDescriptor(\.createdAt)]))) ?? []
    }

    func document(_ meetingID: UUID, _ kind: DocumentKind) -> Document? {
        let raw = kind.rawValue
        return try? context.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw })).first
    }

    func documentRevision(_ id: UUID) -> DocumentRevision? {
        try? context.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.id == id })).first
    }

    func documentRevisions(_ meetingID: UUID, _ kind: DocumentKind? = nil) -> [DocumentRevision] {
        let all = (try? context.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.meetingID == meetingID }))) ?? []
        return kind.map { k in all.filter { $0.kind == k } } ?? all
    }

    func transcriptRevisions(_ meetingID: UUID) -> [TranscriptRevision] {
        (try? context.fetch(FetchDescriptor<TranscriptRevision>(predicate: #Predicate { $0.meetingID == meetingID }))) ?? []
    }

    func jobs(_ meetingID: UUID) -> [ProcessingJob] {
        (try? context.fetch(FetchDescriptor<ProcessingJob>(predicate: #Predicate { $0.meetingID == meetingID }))) ?? []
    }

    func hasPendingWork(_ meetingID: UUID) -> Bool {
        pendingUpload(meetingID).map { $0.state != .verified } == true
            || !intents(meetingID).isEmpty
            || edits(meetingID).contains { $0.hasPendingWork || $0.conflictCurrentRevision != nil }
            || !mutations(meetingID).isEmpty
    }

    // MARK: Orphans and local deletion (ios.md §5, §10)

    func markOrphaned(_ meetingID: UUID, uploads: (any UploadManaging)?) {
        uploads?.cancel(meetingID: meetingID)
        meeting(meetingID)?.localPhase = .orphaned
        save()
    }

    func purgeMeeting(_ meetingID: UUID, uploads: (any UploadManaging)?) {
        uploads?.cancel(meetingID: meetingID)
        SyncFiles.recordingsDirs(meetingID).forEach(SyncFiles.remove)
        if let uploadID = pendingUpload(meetingID)?.uploadID { SyncFiles.remove(SyncFiles.stagingDir(uploadID)) }
        let revisions = documentRevisions(meetingID)
        SyncFiles.removeExports(revisionIDs: revisions.map(\.id))
        localRecording(meetingID).map(context.delete)
        pendingUpload(meetingID).map(context.delete)
        intents(meetingID).forEach(context.delete)
        edits(meetingID).forEach(context.delete)
        mutations(meetingID).forEach(context.delete)
        revisions.forEach(context.delete)
        transcriptRevisions(meetingID).forEach(context.delete)
        jobs(meetingID).forEach(context.delete)
        for kind in [DocumentKind.manualNotes, .aiNotes, .mom] { document(meetingID, kind).map(context.delete) }
        meeting(meetingID).map(context.delete)
        save()
    }

    // 404 on a meeting-scoped read or write: confirm before acting (ios.md §10 Pull).
    func confirmMissing(_ meetingID: UUID, api: APIClient, uploads: (any UploadManaging)?) async {
        do {
            let dto = try await api.send(Endpoints.meeting(meetingID)).meeting
            if let m = meeting(meetingID) { MeetingMirror(store: self).apply(dto, to: m) }
            save()
        } catch APIError.notFound {
            let keepsLocalData = hasPendingWork(meetingID) || localRecording(meetingID) != nil || SyncFiles.hasLocalAudio(meetingID)
            if keepsLocalData { markOrphaned(meetingID, uploads: uploads) } else { purgeMeeting(meetingID, uploads: uploads) }
        } catch {
            SyncLog.logger.info("confirm missing deferred: \(error.localizedDescription, privacy: .public)")
        }
    }
}
