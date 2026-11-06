import Foundation
import SwiftData
import SwiftUI

enum FolderFilter: Hashable, Sendable {
    case all, unfiled, folder(UUID)
}

struct MeetingStatusInput: Equatable, Sendable {
    var localPhase: LocalPhase = .none
    var failedStage: String?
    var transcriptionErrorCategory: String?
    var activeJobs: [JobType] = []
    var momReview: ReviewStatus?
    var pipeline: PipelineState?
    var hasTranscript = false
    var isServerCreated = true
    var livePhase: RecordingPhase?
    var uploadProgress: Double?
    var isOnline = true
}

enum MeetingStatus {
    // Priority order from library.md "Row status": the single most relevant chip.
    static func chip(for input: MeetingStatusInput) -> ChipStatus? {
        if let live = input.livePhase, live.hasLiveSession {
            return live == .interrupted ? .interrupted : .recording
        }
        if input.localPhase == .recording { return .interrupted }
        if input.localPhase == .orphaned { return .orphaned }
        if input.transcriptionErrorCategory == "no_speech" { return .noSpeech }
        if let stage = input.failedStage.flatMap(FailedStage.init(rawValue:)) { return .failed(stage) }
        if input.localPhase == .rejected { return .failed(.upload) }
        if input.localPhase == .waitingStorage { return .waitingStorage }
        let uploadPhases: Set<LocalPhase> = [.merging, .queued, .uploading, .verifying]
        if input.localPhase == .waitingConnection || (!input.isOnline && uploadPhases.contains(input.localPhase)) {
            return .waitingConnection
        }
        switch input.localPhase {
        case .waitingEntitlement: return .planExpired
        case .waitingQuota: return .waitingQuota
        case .merging, .queued: return .queued
        case .uploading: return .uploading(percent: Int(((input.uploadProgress ?? 0) * 100).rounded(.down)))
        case .verifying: return .verifying
        default: break
        }
        if input.activeJobs.contains(.transcribe) || input.pipeline == .transcribing { return .transcribing }
        if input.activeJobs.contains(.generateNotes) { return .generatingNotes }
        if input.activeJobs.contains(.generateMom) { return .generatingMom }
        switch input.momReview {
        case .reviewed?: return .reviewed
        case .draft?: return .momDraft
        case nil: break
        }
        if input.pipeline == .notesReady { return .notesReady }
        if input.hasTranscript || input.pipeline == .generatingNotes || input.pipeline == .notesFailed { return .transcriptReady }
        if !input.isServerCreated && input.localPhase == .none { return .localOnly }
        return nil
    }

    static func isActive(_ input: MeetingStatusInput) -> Bool {
        !input.activeJobs.isEmpty || [.queued, .uploading, .verifying].contains(input.localPhase)
    }
}

struct MeetingRowData: Identifiable, Equatable, Sendable {
    let id: UUID
    let title: String
    let date: Date
    let durationMs: Int?
    let source: MeetingSource
    let status: ChipStatus?
    let folderName: String?
    let tagNames: [String]
}

@MainActor @Observable
final class LibraryModel {
    enum Sheet: Identifiable, Equatable {
        case manageFolders, manageTags, moveMeeting(UUID), tagMeeting(UUID)
        var id: String {
            switch self {
            case .manageFolders: "folders"
            case .manageTags: "tags"
            case .moveMeeting(let id): "move-\(id)"
            case .tagMeeting(let id): "tag-\(id)"
            }
        }
    }

    var searchText = ""
    var folderFilter: FolderFilter = .all
    var tagFilter: UUID?
    var sheet: Sheet?
    var showImporter = false
    var pendingDelete: UUID?
    var renameTarget: UUID?
    var renameText = ""
    var offlineBannerDismissed = false
    var discardRecoveryConfirm = false
    var orphanDiscardTarget: UUID?
    var isRecovering = false
    private(set) var contentResults: [MeetingListItemDTO] = []
    private(set) var lastOpenedID: UUID?

    var query: String { searchText.trimmingCharacters(in: .whitespacesAndNewlines) }
    var isFiltering: Bool { folderFilter != .all || tagFilter != nil }
    var isSearchingOrFiltering: Bool { !query.isEmpty || isFiltering }

    func matchesFilters(folderID: UUID?, tagIDs: [UUID]) -> Bool {
        switch folderFilter {
        case .all: break
        case .unfiled: if folderID != nil { return false }
        case .folder(let id): if folderID != id { return false }
        }
        if let tagFilter, !tagIDs.contains(tagFilter) { return false }
        return true
    }

    func clearFilters() {
        searchText = ""
        folderFilter = .all
        tagFilter = nil
    }

    func runServerSearch(app: AppModel) async {
        let q = query
        guard !q.isEmpty, q.count <= 200, app.isOnline else {
            contentResults = []
            return
        }
        try? await Task.sleep(for: .milliseconds(300))
        guard !Task.isCancelled else { return }
        guard let results = try? await app.sync.search(q), !Task.isCancelled else { return }
        contentResults = results
    }

    func open(_ id: UUID, app: AppModel) {
        lastOpenedID = id
        app.openMeeting(id)
    }

    func openRemote(_ id: UUID, app: AppModel) async {
        await app.sync.refreshMeeting(id)
        open(id, app: app)
    }

    func refresh(app: AppModel, meetings: [Meeting]) async {
        app.poller.reset()
        await app.sync.fullPull()
        if meetings.contains(where: { $0.localPhase == .waitingQuota || $0.localPhase == .waitingEntitlement }) {
            await app.refreshUsage()
        }
    }

    // MARK: Mutations (SyncEngine.enqueue applies the local effects, then pushes)

    func rename(_ meeting: Meeting, to title: String, app: AppModel) {
        let trimmed = String(title.trimmingCharacters(in: .whitespacesAndNewlines).prefix(200))
        guard trimmed != meeting.title else { return }
        var patch = MeetingPatchRequest(expectedMetadataRevision: meeting.metadataRevision)
        patch.title = .set(trimmed)
        app.sync.enqueue(.patchMeeting(meeting.id, patch))
    }

    func move(_ meeting: Meeting, to folder: Folder?, app: AppModel) {
        guard meeting.folder?.id != folder?.id else { return }
        var patch = MeetingPatchRequest()
        patch.folderId = folder.map { .set($0.id) } ?? .clear
        app.sync.enqueue(.patchMeeting(meeting.id, patch))
        let name = folder?.name ?? LS("library.filter.noFolder", table: .library)
        app.toasts.show(L("folder.moved", table: .library, name))
    }

    func setTags(_ meeting: Meeting, to tagIDs: [UUID], app: AppModel) {
        var patch = MeetingPatchRequest()
        var seen = Set<UUID>()
        patch.tagIds = .set(tagIDs.filter { seen.insert($0).inserted })
        app.sync.enqueue(.patchMeeting(meeting.id, patch))
    }

    func delete(_ id: UUID, app: AppModel) {
        app.uploads.cancel(meetingID: id)
        app.sync.enqueue(.deleteMeeting(id))
    }

    // MARK: Recovery

    func recover(_ recording: LocalRecording, app: AppModel) async {
        isRecovering = true
        let duration = Self.recoverableMs(recording)
        await app.recorder.recover(recording)
        isRecovering = false
        app.pendingRecovery = nil
        app.toasts.show(L("library.recovery.done", table: .library, Formatting.timer(ms: duration)))
    }

    func discardRecovery(_ recording: LocalRecording, app: AppModel) {
        app.recorder.discard(recording)
        app.pendingRecovery = nil
    }

    static func recoverableMs(_ recording: LocalRecording) -> Int {
        max(recording.activeDurationMs, recording.segments.reduce(0) { $0 + $1.durationMs })
    }
}

enum NameValidation: Equatable {
    case valid(String), empty, duplicate

    static let maxLength = 60

    static func check(_ raw: String, existing: [(id: UUID, name: String)], excluding: UUID? = nil) -> NameValidation {
        let name = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { return .empty }
        let clash = existing.contains { $0.id != excluding && $0.name.compare(name, options: [.caseInsensitive]) == .orderedSame }
        return clash ? .duplicate : .valid(String(name.prefix(maxLength)))
    }
}
