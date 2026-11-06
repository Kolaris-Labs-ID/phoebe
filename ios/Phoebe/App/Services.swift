import Foundation
import SwiftData

// Protocol seams implemented by the Audio / Sync agents. Concrete types are wired in
// LiveServices.swift; placeholders below keep the app building until then.

@MainActor
struct ServiceEnvironment {
    let api: APIClient
    let context: ModelContext
    let userID: UUID
    let auth: AuthStore
    unowned let app: AppModel
}

@MainActor
struct Services {
    var recorder: any RecordingEngineAPI
    var uploads: any UploadManaging
    var sync: any SyncEngineAPI
    var poller: any PollingAPI
    var events: any EventQueueAPI

    static func placeholder() -> Services {
        Services(
            recorder: PlaceholderRecordingEngine(),
            uploads: PlaceholderUploadManager(),
            sync: PlaceholderSyncEngine(),
            poller: PlaceholderPoller(),
            events: PlaceholderEventQueue()
        )
    }
}

// MARK: - Recording (ios.md §6)

enum RecordingPhase: String, Sendable, CaseIterable {
    case idle, ready, starting, recording, paused, interrupted, stopping, stopped

    var hasLiveSession: Bool {
        switch self {
        case .starting, .recording, .paused, .interrupted, .stopping: true
        default: false
        }
    }

    var buttonState: RecordButtonState {
        switch self {
        case .idle, .ready, .stopped: .ready
        case .starting: .starting
        case .recording: .recording
        case .paused, .interrupted: .paused
        case .stopping: .stopping
        }
    }
}

enum MicPermission: Sendable {
    case undetermined, granted, denied
}

enum RecordingBanner: Equatable, Sendable {
    case startFailed
    case writeFailed
    case interrupted(savedMs: Int)
    case interruptionWaiting
    case mediaReset
    case routeChanged(inputName: String)
    case noInput
    case storageLow(freeBytes: Int64)
    case storageStopped(savedMs: Int)
    case storagePreflight(requiredBytes: Int64)
    case limitNear(limitMinutes: Int, minutesLeft: Int)
    case limitReached(limitMinutes: Int)
}

struct RecordingSummary: Equatable, Sendable {
    let meetingID: UUID
    let durationMs: Int
    let segmentCount: Int
}

@MainActor
protocol RecordingEngineAPI: AnyObject {
    var phase: RecordingPhase { get }
    var meetingID: UUID? { get }
    var activeDurationMs: Int { get }
    var segmentCount: Int { get }
    var inputLevel: Float { get }
    var banner: RecordingBanner? { get }
    var micPermission: MicPermission { get }
    var canResume: Bool { get }

    func refreshPermission()
    func requestMicPermission() async -> Bool
    func prepare() async
    func start(meetingID: UUID) async
    func pause()
    func resume() async
    func stop() async -> RecordingSummary?
    func dismissBanner()

    func recoverOnLaunch() async -> LocalRecording?
    func recover(_ recording: LocalRecording) async
    func discard(_ recording: LocalRecording)
    func retryPendingMerges() async
}

// MARK: - Upload (ios.md §8)

@MainActor
protocol UploadManaging: AnyObject {
    func resumeAll() async
    func kick(meetingID: UUID) async
    func retry(meetingID: UUID) async
    func cancel(meetingID: UUID)
    func progress(meetingID: UUID) -> Double?
    func retryWaitingIntents(usage: UsageDTO) async
}

// MARK: - Sync (ios.md §10)

enum OutboxMutation: Sendable, Equatable {
    case createMeeting(UUID)
    case patchMeeting(UUID, MeetingPatchRequest)
    case deleteMeeting(UUID)
    case deleteAudio(UUID)
    case createFolder(id: UUID, name: String)
    case renameFolder(id: UUID, name: String)
    case deleteFolder(UUID)
    case createTag(id: UUID, name: String)
    case renameTag(id: UUID, name: String)
    case deleteTag(UUID)
}

enum ConflictResolution: Sendable {
    case keepMine, useLatest
}

@MainActor
protocol SyncEngineAPI: AnyObject {
    var isPulling: Bool { get }
    var lastPullFailed: Bool { get }

    func fullPull() async
    @discardableResult func pollTick() async -> Bool
    func refreshMeeting(_ id: UUID) async
    func pushAll() async
    func enqueue(_ mutation: OutboxMutation)

    func saveDraft(meetingID: UUID, kind: DocumentKind, wireContent: Data, loadedRevision: Int)
    func flushEdits(meetingID: UUID, kinds: [DocumentKind]) async -> Bool
    func resolveDocumentConflict(meetingID: UUID, kind: DocumentKind, _ resolution: ConflictResolution) async
    func resolveMetadataConflict(meetingID: UUID, _ resolution: ConflictResolution) async
    func setReview(meetingID: UUID, kind: DocumentKind, status: ReviewStatus) async throws
    func requestGeneration(meetingID: UUID, type: IntentType, body: Data) async throws

    func search(_ query: String) async throws -> [MeetingListItemDTO]
    func keepOrphanAsNewMeeting(_ id: UUID) async -> UUID?
    func discardOrphan(_ id: UUID)
}

// MARK: - Polling (ios.md §11)

enum PollTarget: Hashable, Sendable {
    case library
    case meeting(UUID)
}

@MainActor
protocol PollingAPI: AnyObject {
    func start(_ target: PollTarget)
    func stop(_ target: PollTarget)
    func reset()
    func setForeground(_ active: Bool)
}

// MARK: - Events (ios.md §14)

@MainActor
protocol EventQueueAPI: AnyObject {
    func log(_ event: AnalyticsEvent, meetingID: UUID?)
    func flush() async
}

// MARK: - Placeholders

@MainActor @Observable
final class PlaceholderRecordingEngine: RecordingEngineAPI {
    private(set) var phase: RecordingPhase = .idle
    private(set) var meetingID: UUID?
    private(set) var activeDurationMs = 0
    private(set) var segmentCount = 0
    private(set) var inputLevel: Float = 0
    private(set) var banner: RecordingBanner?
    private(set) var micPermission: MicPermission = .undetermined
    private(set) var canResume = false

    func refreshPermission() {}
    func requestMicPermission() async -> Bool { false }
    func prepare() async { phase = .ready }
    func start(meetingID: UUID) async { banner = .startFailed }
    func pause() {}
    func resume() async {}
    func stop() async -> RecordingSummary? { nil }
    func dismissBanner() { banner = nil }
    func recoverOnLaunch() async -> LocalRecording? { nil }
    func recover(_ recording: LocalRecording) async {}
    func discard(_ recording: LocalRecording) {}
    func retryPendingMerges() async {}
}

@MainActor
final class PlaceholderUploadManager: UploadManaging {
    func resumeAll() async {}
    func kick(meetingID: UUID) async {}
    func retry(meetingID: UUID) async {}
    func cancel(meetingID: UUID) {}
    func progress(meetingID: UUID) -> Double? { nil }
    func retryWaitingIntents(usage: UsageDTO) async {}
}

@MainActor @Observable
final class PlaceholderSyncEngine: SyncEngineAPI {
    private(set) var isPulling = false
    private(set) var lastPullFailed = false

    func fullPull() async {}
    func pollTick() async -> Bool { false }
    func refreshMeeting(_ id: UUID) async {}
    func pushAll() async {}
    func enqueue(_ mutation: OutboxMutation) {}
    func saveDraft(meetingID: UUID, kind: DocumentKind, wireContent: Data, loadedRevision: Int) {}
    func flushEdits(meetingID: UUID, kinds: [DocumentKind]) async -> Bool { false }
    func resolveDocumentConflict(meetingID: UUID, kind: DocumentKind, _ resolution: ConflictResolution) async {}
    func resolveMetadataConflict(meetingID: UUID, _ resolution: ConflictResolution) async {}
    func setReview(meetingID: UUID, kind: DocumentKind, status: ReviewStatus) async throws { throw APIError.unavailable }
    func requestGeneration(meetingID: UUID, type: IntentType, body: Data) async throws { throw APIError.unavailable }
    func search(_ query: String) async throws -> [MeetingListItemDTO] { [] }
    func keepOrphanAsNewMeeting(_ id: UUID) async -> UUID? { nil }
    func discardOrphan(_ id: UUID) {}
}

@MainActor
final class PlaceholderPoller: PollingAPI {
    func start(_ target: PollTarget) {}
    func stop(_ target: PollTarget) {}
    func reset() {}
    func setForeground(_ active: Bool) {}
}

@MainActor
final class PlaceholderEventQueue: EventQueueAPI {
    func log(_ event: AnalyticsEvent, meetingID: UUID?) {}
    func flush() async {}
}
