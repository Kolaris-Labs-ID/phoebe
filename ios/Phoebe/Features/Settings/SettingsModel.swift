import Foundation
import SwiftData

struct UsageSummary: Equatable {
    enum Condition: Equatable { case normal, low, exhausted, planEnded }

    let usedMinutes: Int
    let quotaMinutes: Int
    let leftMinutes: Int
    let reservedMinutes: Int
    let fraction: Double
    let condition: Condition
    let isTrial: Bool
    let periodEnd: Date?

    init(_ usage: UsageDTO) {
        let quota = usage.entitlement.quotaSeconds
        usedMinutes = Formatting.quotaMinutes(seconds: usage.consumedSeconds)
        quotaMinutes = Formatting.quotaMinutes(seconds: quota)
        leftMinutes = Formatting.quotaMinutes(seconds: usage.remainingSeconds)
        reservedMinutes = Formatting.quotaMinutes(seconds: usage.reservedSeconds)
        fraction = quota > 0 ? min(1, Double(usage.consumedSeconds) / Double(quota)) : 1
        isTrial = usage.entitlement.plan == "trial"
        periodEnd = usage.entitlement.periodEnd
        if usage.entitlement.status != .active {
            condition = .planEnded
        } else if usage.remainingSeconds <= 0 {
            condition = .exhausted
        } else if usage.remainingSeconds * 10 <= quota {
            condition = .low
        } else {
            condition = .normal
        }
    }
}

@MainActor
enum ProfilePreferences {
    private static func pendingKey(_ userID: UUID) -> String { "phoebe.pendingOutputLanguage.\(userID.lower)" }

    static var appOutputLanguage: OutputLanguage {
        Bundle.main.preferredLocalizations.first == "id" ? .id : .en
    }

    static func profile(app: AppModel) -> UserProfile? {
        guard let context = app.modelContext, let id = app.user?.id else { return nil }
        return try? context.fetch(FetchDescriptor<UserProfile>(predicate: #Predicate { $0.id == id })).first
    }

    static func pendingOutputLanguage(app: AppModel) -> OutputLanguage? {
        guard let id = app.user?.id else { return nil }
        return UserDefaults.standard.string(forKey: pendingKey(id)).flatMap(OutputLanguage.init(rawValue:))
    }

    static func outputLanguage(app: AppModel) -> OutputLanguage {
        pendingOutputLanguage(app: app) ?? app.user.flatMap { OutputLanguage(rawValue: $0.language) } ?? appOutputLanguage
    }

    static func transcriptionLanguage(app: AppModel) -> TranscriptionLanguage {
        profile(app: app)?.transcriptionLanguage ?? .auto
    }

    static func setTranscriptionLanguage(_ language: TranscriptionLanguage, app: AppModel) {
        guard let profile = profile(app: app) else { return }
        profile.transcriptionLanguage = language
        try? app.modelContext?.save()
    }

    static func setOutputLanguage(_ language: OutputLanguage, app: AppModel) async {
        guard let id = app.user?.id else { return }
        UserDefaults.standard.set(language.rawValue, forKey: pendingKey(id))
        if let profile = profile(app: app) {
            profile.language = language.rawValue
            try? app.modelContext?.save()
        }
        await pushPending(app: app)
    }

    static func pushPending(app: AppModel) async {
        guard let id = app.user?.id, let pending = pendingOutputLanguage(app: app) else { return }
        guard let envelope = try? await app.api.send(Endpoints.updateMe(UpdateMeRequest(language: pending))) else { return }
        if pendingOutputLanguage(app: app) == pending { UserDefaults.standard.removeObject(forKey: pendingKey(id)) }
        app.updateUser(envelope.user)
    }

    static func clearPending(userID: UUID) {
        UserDefaults.standard.removeObject(forKey: pendingKey(userID))
    }
}

enum LocalAudioFiles {
    static func url(forStoredPath path: String) -> URL {
        path.hasPrefix("/") ? URL(filePath: path) : URL.applicationSupportDirectory.appending(path: path)
    }

    static func recordingDirectories(meetingID: UUID) -> [URL] {
        let root = URL.applicationSupportDirectory.appending(path: "recordings", directoryHint: .isDirectory)
        return Set([meetingID.lower, meetingID.uuidString]).map { root.appending(path: $0, directoryHint: .isDirectory) }
    }
}

@MainActor @Observable
final class SettingsModel {
    enum UsageState: Equatable { case loading, loaded, error }

    private(set) var usageState: UsageState = .loading
    private(set) var outputSyncPending = false
    private(set) var isSigningOut = false
    private(set) var isDeletingAccount = false
    var showDeleteFailed = false
    var showSignOutConfirm = false
    var showDeleteStep1 = false
    var showDeleteStep2 = false
    var deleteConfirmText = ""
    var showLocalAudioConfirm = false
    private(set) var eligibleLocalAudio: [URL] = []

    func refreshUsage(app: AppModel) async {
        usageState = await app.refreshUsage() == nil && app.isOnline ? .error : .loaded
    }

    func isStale(app: AppModel) -> Bool { !app.isOnline && app.usage != nil }

    func refreshSyncPending(app: AppModel) {
        outputSyncPending = ProfilePreferences.pendingOutputLanguage(app: app) != nil
    }

    func setOutputLanguage(_ language: OutputLanguage, app: AppModel) async {
        outputSyncPending = true
        await ProfilePreferences.setOutputLanguage(language, app: app)
        refreshSyncPending(app: app)
    }

    func retryPendingLanguage(app: AppModel) async {
        await ProfilePreferences.pushPending(app: app)
        refreshSyncPending(app: app)
    }

    // MARK: Local audio

    func setDeleteLocalAudio(_ enabled: Bool, profile: UserProfile, context: ModelContext) {
        profile.deleteLocalAudioAfterTranscript = enabled
        try? context.save()
        guard enabled else { return }
        eligibleLocalAudio = Self.eligibleLocalAudioFiles(context: context)
        showLocalAudioConfirm = !eligibleLocalAudio.isEmpty
    }

    var eligibleLocalAudioSize: String {
        let bytes = eligibleLocalAudio.reduce(Int64(0)) { sum, url in
            sum + Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
        }
        return ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }

    func deleteEligibleLocalAudio() {
        for url in eligibleLocalAudio { try? FileManager.default.removeItem(at: url) }
        eligibleLocalAudio = []
    }

    // Only verified uploads of transcribed meetings: before that the local file may be the only full copy.
    static func eligibleLocalAudioFiles(context: ModelContext) -> [URL] {
        let verified = PendingUploadState.verified.rawValue
        let uploads = (try? context.fetch(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.stateRaw == verified }))) ?? []
        let meetings = (try? context.fetch(FetchDescriptor<Meeting>())) ?? []
        let transcribed = Set(meetings.filter(Self.hasTranscript).map(\.id))
        return uploads
            .filter { transcribed.contains($0.meetingID) }
            .map { LocalAudioFiles.url(forStoredPath: $0.filePath) }
            .filter { FileManager.default.fileExists(atPath: $0.path(percentEncoded: false)) }
    }

    static func hasTranscript(_ meeting: Meeting) -> Bool {
        if meeting.hasTranscript { return true }
        switch meeting.pipeline {
        case .transcriptReady, .generatingNotes, .notesFailed, .notesReady: return true
        default: return false
        }
    }

    // MARK: Account

    func pendingUploadCount(context: ModelContext) -> Int {
        let verified = PendingUploadState.verified.rawValue
        return (try? context.fetchCount(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.stateRaw != verified }))) ?? 0
    }

    func signOut(app: AppModel) async {
        isSigningOut = true
        await app.signOut(reason: .userInitiated)
        isSigningOut = false
    }

    func deleteConfirmMatches(word: String) -> Bool {
        deleteConfirmText.trimmingCharacters(in: .whitespacesAndNewlines).uppercased() == word.uppercased()
    }

    func deleteAccount(app: AppModel) async {
        isDeletingAccount = true
        do {
            _ = try await app.api.send(Endpoints.deleteAccount())
        } catch {
            isDeletingAccount = false
            showDeleteFailed = true
            return
        }
        let userID = app.user?.id
        var meetingIDs = Set<UUID>()
        if let context = app.modelContext {
            meetingIDs.formUnion(((try? context.fetch(FetchDescriptor<Meeting>())) ?? []).map(\.id))
            meetingIDs.formUnion(((try? context.fetch(FetchDescriptor<LocalRecording>())) ?? []).map(\.meetingID))
        }
        for id in meetingIDs { app.uploads.cancel(meetingID: id) }
        weak let container = app.modelContainer
        await app.signOut(reason: .accountDeleted, callServer: false)
        // Unlinking the store while signed-in views still hold @Query on it crashes SwiftData; wait for the tree to release it.
        for _ in 0..<30 where container != nil {
            try? await Task.sleep(for: .milliseconds(100))
        }
        Self.wipeLocalData(userID: userID, meetingIDs: meetingIDs)
        app.auth.set(nil, for: .pendingDisplayName)
        isDeletingAccount = false
        app.advanceOnboarding(to: .welcome)
    }

    private static func wipeLocalData(userID: UUID?, meetingIDs: Set<UUID>) {
        let fm = FileManager.default
        for id in meetingIDs {
            for dir in LocalAudioFiles.recordingDirectories(meetingID: id) { try? fm.removeItem(at: dir) }
        }
        try? fm.removeItem(at: URL.cachesDirectory.appending(path: "uploads", directoryHint: .isDirectory))
        try? fm.removeItem(at: URL.cachesDirectory.appending(path: "exports", directoryHint: .isDirectory))
        if let userID {
            try? fm.removeItem(at: URL.applicationSupportDirectory.appending(path: userID.lower, directoryHint: .isDirectory))
            ProfilePreferences.clearPending(userID: userID)
        }
    }
}
