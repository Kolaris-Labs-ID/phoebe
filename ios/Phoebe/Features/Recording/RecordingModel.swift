import Foundation
import SwiftData
import SwiftUI

enum RecordingNextStep: Equatable {
    case upload, offline, quota, plan

    static func make(isOnline: Bool, usage: UsageDTO?, durationMs: Int) -> RecordingNextStep {
        if let usage {
            if usage.entitlement.status != .active { return .plan }
            if usage.remainingSeconds < Int((Double(durationMs) / 1000).rounded(.up)) { return .quota }
        }
        return isOnline ? .upload : .offline
    }

    var key: String {
        switch self {
        case .upload: "rec.stopped.next.upload"
        case .offline: "rec.stopped.next.offline"
        case .quota: "rec.stopped.next.quota"
        case .plan: "rec.stopped.next.plan"
        }
    }
}

enum ReadyNotice: Equatable {
    case plan, quota

    static func make(usage: UsageDTO?) -> ReadyNotice? {
        guard let usage else { return nil }
        if usage.entitlement.status != .active { return .plan }
        if usage.remainingSeconds <= 0 { return .quota }
        return nil
    }
}

struct RecordingBannerContent: Equatable {
    let tone: Tone
    let systemImage: String
    let titleKey: String?
    let messageKey: String
    let closable: Bool
    let retriesStart: Bool
    let autoDismissAfter: Duration?

    static func make(_ banner: RecordingBanner) -> RecordingBannerContent {
        switch banner {
        case .startFailed:
            .init(tone: .danger, systemImage: "exclamationmark.triangle", titleKey: nil, messageKey: "rec.error.start", closable: false, retriesStart: true, autoDismissAfter: nil)
        case .writeFailed:
            .init(tone: .warning, systemImage: "exclamationmark.triangle", titleKey: nil, messageKey: "rec.error.write", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .interrupted:
            .init(tone: .warning, systemImage: "phone.down.circle", titleKey: "rec.interrupt.title", messageKey: "rec.interrupt.body", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .interruptionWaiting:
            .init(tone: .warning, systemImage: "phone.down.circle", titleKey: "rec.interrupt.title", messageKey: "rec.interrupt.waiting", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .mediaReset:
            .init(tone: .warning, systemImage: "arrow.clockwise.circle", titleKey: "rec.reset.title", messageKey: "rec.reset.body", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .routeChanged:
            .init(tone: .info, systemImage: "headphones", titleKey: "rec.route.title", messageKey: "rec.route.body", closable: true, retriesStart: false, autoDismissAfter: .seconds(5))
        case .noInput:
            .init(tone: .warning, systemImage: "mic.slash", titleKey: nil, messageKey: "rec.route.noInput", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .storageLow:
            .init(tone: .warning, systemImage: "externaldrive.badge.exclamationmark", titleKey: nil, messageKey: "rec.storage.low", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .storageStopped:
            .init(tone: .danger, systemImage: "externaldrive.badge.xmark", titleKey: nil, messageKey: "rec.storage.stopped", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .storagePreflight:
            .init(tone: .danger, systemImage: "externaldrive.badge.exclamationmark", titleKey: nil, messageKey: "rec.storage.preflight", closable: true, retriesStart: false, autoDismissAfter: nil)
        case .limitNear:
            .init(tone: .warning, systemImage: "timer", titleKey: nil, messageKey: "rec.limit.near", closable: false, retriesStart: false, autoDismissAfter: nil)
        case .limitReached:
            .init(tone: .info, systemImage: "timer", titleKey: nil, messageKey: "rec.limit.reached", closable: false, retriesStart: false, autoDismissAfter: nil)
        }
    }

    static func message(_ banner: RecordingBanner) -> LocalizedStringResource {
        let key = make(banner).messageKey
        switch banner {
        case .interrupted(let savedMs): return L(key, table: .recording, Formatting.timer(ms: savedMs))
        case .routeChanged(let name): return L(key, table: .recording, name)
        case .storageLow(let free): return L(key, table: .recording, bytes(free))
        case .storageStopped(let savedMs): return L(key, table: .recording, Formatting.timer(ms: savedMs))
        case .storagePreflight(let required): return L(key, table: .recording, bytes(required))
        case .limitNear(let limit, let left): return L(key, table: .recording, Int64(limit), Int64(left))
        case .limitReached(let limit): return L(key, table: .recording, Int64(limit))
        default: return L(key, table: .recording)
        }
    }

    static func bytes(_ value: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: value, countStyle: .file)
    }
}

@MainActor @Observable
final class RecordingModel {
    var title = ""
    var language: TranscriptionLanguage = .auto
    private(set) var meetingID: UUID?
    var notes: [NoteItem] = []
    var draftPoint = ""
    private(set) var notesSaving = false
    var showPermissionSheet = false
    var showStopConfirm = false
    var showRename = false
    var offlineBannerDismissed = false
    private(set) var summary: RecordingSummary?
    private(set) var defaultTitle = ""

    @ObservationIgnored private var saveTask: Task<Void, Never>?
    @ObservationIgnored private var loaded = false

    func load(app: AppModel) {
        guard !loaded else { return }
        loaded = true
        defaultTitle = Self.defaultTitle(for: .now)
        if app.recorder.phase.hasLiveSession, let id = app.recorder.meetingID, let context = app.modelContext {
            meetingID = id
            if let meeting = Self.meeting(id, in: context) {
                title = meeting.title
                language = meeting.transcriptionLanguage
            }
            notes = Self.storedNotes(meetingID: id, in: context)
        } else if let context = app.modelContext, let userID = app.user?.id {
            let profile = try? context.fetch(FetchDescriptor<UserProfile>(predicate: #Predicate { $0.id == userID })).first
            language = profile?.transcriptionLanguage ?? .auto
        }
    }

    static func defaultTitle(for date: Date) -> String {
        LS("rec.title.default", table: .recording, date.formatted(.dateTime.day().month(.abbreviated).hour().minute()))
    }

    var displayTitle: String { title.nonBlank ?? defaultTitle }

    // MARK: Start / pause / stop

    func recordTapped(app: AppModel) async {
        switch app.recorder.micPermission {
        case .undetermined: showPermissionSheet = true
        case .denied: break
        case .granted: await start(app: app)
        }
    }

    func permissionContinue(app: AppModel) async {
        showPermissionSheet = false
        if await app.recorder.requestMicPermission() {
            await start(app: app)
        } else {
            app.recorder.refreshPermission()
        }
    }

    func start(app: AppModel) async {
        guard let context = app.modelContext, !app.recorder.phase.hasLiveSession else { return }
        let id = UUID()
        let meeting = Meeting(id: id, source: .recording, title: displayTitle, startedAt: .now, language: language)
        meeting.localPhase = .recording
        context.insert(meeting)
        try? context.save()

        await app.recorder.start(meetingID: id)

        if app.recorder.phase.hasLiveSession {
            meetingID = id
            app.sync.enqueue(.createMeeting(id))
            if !notes.isEmpty { saveNotesNow(app: app) }
        } else {
            context.delete(meeting)
            try? context.save()
        }
    }

    func mainTapped(app: AppModel) async {
        switch app.recorder.phase {
        case .idle, .ready, .stopped: await recordTapped(app: app)
        case .recording:
            app.recorder.pause()
            saveNotesNow(app: app)
        case .paused, .interrupted:
            guard app.recorder.canResume || app.recorder.phase == .paused else { return }
            await app.recorder.resume()
        case .starting, .stopping: break
        }
    }

    func confirmStop(app: AppModel) async {
        saveNotesNow(app: app)
        if let result = await app.recorder.stop() { finish(app: app, result: result) }
    }

    // Covers both a user stop and an engine stop (storage critical, duration limit).
    func finish(app: AppModel, result: RecordingSummary? = nil) {
        guard summary == nil, let id = meetingID ?? app.recorder.meetingID else { return }
        saveNotesNow(app: app)
        summary = result ?? RecordingSummary(meetingID: id, durationMs: app.recorder.activeDurationMs, segmentCount: app.recorder.segmentCount)
        app.events.log(.recordingSaved, meetingID: id)
    }

    func nextStep(app: AppModel) -> RecordingNextStep {
        RecordingNextStep.make(isOnline: app.isOnline, usage: app.usage, durationMs: summary?.durationMs ?? 0)
    }

    // MARK: Title

    func rename(to newTitle: String, app: AppModel) {
        let trimmed = String(newTitle.trimmingCharacters(in: .whitespacesAndNewlines).prefix(200))
        title = trimmed
        guard let id = meetingID, let context = app.modelContext, let meeting = Self.meeting(id, in: context) else { return }
        let value = trimmed.nonBlank ?? defaultTitle
        guard value != meeting.title else { return }
        guard meeting.isServerCreated else {
            // The pending create reads the Meeting row when it is sent, so the new title travels with it.
            meeting.title = value
            meeting.refreshSearchText()
            try? context.save()
            return
        }
        app.sync.enqueue(.patchMeeting(id, MeetingPatchRequest(expectedMetadataRevision: meeting.metadataRevision, title: .set(value))))
    }

    // MARK: Notes

    func addPoint(atMs: Int, app: AppModel) {
        let text = draftPoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        notes.append(NoteItem.new(text: text, atMs: atMs))
        draftPoint = ""
        saveNotesNow(app: app)
    }

    func updatePoint(_ id: String, text: String, app: AppModel) {
        guard let index = notes.firstIndex(where: { $0.id == id }), notes[index].text != text else { return }
        notes[index].text = text
        scheduleSave(app: app)
    }

    func scheduleSave(app: AppModel) {
        notesSaving = true
        saveTask?.cancel()
        saveTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            self?.saveNotesNow(app: app)
        }
    }

    func saveNotesNow(app: AppModel) {
        saveTask?.cancel()
        saveTask = nil
        defer { notesSaving = false }
        guard let id = meetingID, let context = app.modelContext else { return }
        let content = DocumentContentValue.manualNotes(ManualNotesContent(items: notes))
        guard let data = try? content.wireData() else { return }
        let loaded = Self.document(meetingID: id, kind: .manualNotes, in: context)?.currentRevisionNumber ?? 0
        app.sync.saveDraft(meetingID: id, kind: .manualNotes, wireContent: data, loadedRevision: loaded)
    }

    // MARK: Fetch helpers

    static func meeting(_ id: UUID, in context: ModelContext) -> Meeting? {
        try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
    }

    static func document(meetingID: UUID, kind: DocumentKind, in context: ModelContext) -> Document? {
        let raw = kind.rawValue
        return try? context.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw })).first
    }

    static func storedNotes(meetingID: UUID, in context: ModelContext) -> [NoteItem] {
        let raw = DocumentKind.manualNotes.rawValue
        let edit = try? context.fetch(FetchDescriptor<PendingDocumentEdit>(predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw })).first
        var descriptor = FetchDescriptor<DocumentRevision>(
            predicate: #Predicate { $0.meetingID == meetingID && $0.kindRaw == raw && $0.content != nil },
            sortBy: [SortDescriptor(\.revisionNumber, order: .reverse)]
        )
        descriptor.fetchLimit = 1
        let data = edit?.content ?? edit?.inFlightContent ?? (try? context.fetch(descriptor).first)?.content
        guard let data, case .manualNotes(let content)? = try? DocumentContentValue.decode(kind: .manualNotes, from: data) else { return [] }
        return content.items
    }
}
