import Foundation
import SwiftData

enum ImportFailure: Error, Equatable, Sendable {
    case type, corrupt, empty, storage
    case tooLong(durationMs: Int)
    case tooBig(sizeBytes: Int)
}

struct ImportLimits: Equatable, Sendable {
    let maxUploadBytes: Int
    let maxDurationMs: Int
}

struct ImportCandidate: Identifiable, Equatable, Sendable {
    let sourceURL: URL
    let fileName: String
    let fileExtension: String
    let contentType: String
    let sizeBytes: Int
    let durationMs: Int

    var id: URL { sourceURL }
}

struct ImportedFile: Equatable, Sendable {
    let relativePath: String
    let sizeBytes: Int
    let sha256: String
}

// Seam onto Audio's ImportValidator + ImportCopier (ios.md §7); implemented by AudioImportService.
@MainActor
protocol ImportServicing: AnyObject {
    func validate(_ url: URL, limits: ImportLimits) async -> Result<ImportCandidate, ImportFailure>
    func copy(_ candidate: ImportCandidate, meetingID: UUID, progress: @escaping @MainActor (Double) -> Void) async -> Result<ImportedFile, ImportFailure>
}

@MainActor @Observable
final class ImportModel {
    enum Phase: Equatable {
        case idle
        case validating(fileName: String)
        case failed(ImportFailure)
        case copying(fileName: String, progress: Double)
    }

    static let defaultMaxUploadBytes = 262_144_000

    private(set) var phase: Phase = .idle
    var candidate: ImportCandidate?
    @ObservationIgnored private let service: any ImportServicing
    @ObservationIgnored private var scopedURL: URL?

    init(service: any ImportServicing = AudioImportService()) {
        self.service = service
    }

    var isBusy: Bool {
        switch phase {
        case .validating, .copying: true
        default: false
        }
    }

    static func limits(app: AppModel) -> ImportLimits {
        ImportLimits(maxUploadBytes: app.serverConfig?.maxUploadBytes ?? defaultMaxUploadBytes, maxDurationMs: app.maxAudioDurationMs)
    }

    // Access stays open from validation until the copy finishes: the user confirms in between.
    func begin(_ url: URL, app: AppModel) async {
        releaseAccess()
        if url.startAccessingSecurityScopedResource() { scopedURL = url }
        phase = .validating(fileName: url.lastPathComponent)
        switch await service.validate(url, limits: Self.limits(app: app)) {
        case .success(let validated):
            phase = .idle
            candidate = validated
        case .failure(let failure):
            releaseAccess()
            phase = .failed(failure)
        }
    }

    func cancel() {
        candidate = nil
        releaseAccess()
    }

    func dismissFailure() {
        if case .failed = phase { phase = .idle }
    }

    func confirm(language: TranscriptionLanguage, app: AppModel) async {
        guard let candidate, let context = app.modelContext, let userID = app.user?.id else { return }
        self.candidate = nil
        let meetingID = UUID()
        phase = .copying(fileName: candidate.fileName, progress: 0)
        let result = await service.copy(candidate, meetingID: meetingID) { [weak self] value in
            guard let self, case .copying = self.phase else { return }
            self.phase = .copying(fileName: candidate.fileName, progress: value)
        }
        releaseAccess()
        switch result {
        case .failure(let failure):
            phase = .failed(failure)
        case .success(let file):
            Self.createMeeting(id: meetingID, candidate: candidate, file: file, language: language, userID: userID, context: context)
            phase = .idle
            app.sync.enqueue(.createMeeting(meetingID))
            await app.uploads.kick(meetingID: meetingID)
        }
    }

    // One save: the meeting, its upload and its transcribe intent appear together or not at all.
    static func createMeeting(id: UUID, candidate: ImportCandidate, file: ImportedFile, language: TranscriptionLanguage, userID: UUID, context: ModelContext) {
        let title = (candidate.fileName as NSString).deletingPathExtension
        let meeting = Meeting(id: id, source: .import, title: title, startedAt: nil, language: language)
        meeting.durationMs = candidate.durationMs
        meeting.localPhase = .queued
        context.insert(meeting)
        context.insert(PendingUpload(
            meetingID: id,
            ownerUserID: userID,
            filePath: file.relativePath,
            sizeBytes: file.sizeBytes,
            sha256: file.sha256,
            contentType: candidate.contentType,
            durationMs: candidate.durationMs
        ))
        context.insert(PendingIntent.transcribe(meetingID: id, language: language))
        try? context.save()
    }

    private func releaseAccess() {
        scopedURL?.stopAccessingSecurityScopedResource()
        scopedURL = nil
    }
}
