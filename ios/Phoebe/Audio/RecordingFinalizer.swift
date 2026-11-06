import Foundation
import SwiftData
import UIKit

// stopped → merge → (one save) mergedFileName + PendingUpload + PendingIntent(.transcribe) + queued.
@MainActor
final class RecordingFinalizer {
    private let context: ModelContext
    private let root: URL
    private let storage: any StorageProviding
    private let config: RecordingConfig
    private let merger: SegmentMerger
    private let onQueued: @MainActor (UUID) -> Void
    private var inFlight: Set<UUID> = []

    init(
        context: ModelContext,
        root: URL = RecordingPaths.root,
        storage: any StorageProviding = StorageMonitor(),
        config: RecordingConfig = .standard,
        merger: SegmentMerger = SegmentMerger(),
        onQueued: @escaping @MainActor (UUID) -> Void = { _ in }
    ) {
        self.context = context
        self.root = root
        self.storage = storage
        self.config = config
        self.merger = merger
        self.onQueued = onQueued
    }

    var base: URL { root.deletingLastPathComponent() }

    func isFinalizing(_ meetingID: UUID) -> Bool { inFlight.contains(meetingID) }

    func finalize(_ recording: LocalRecording) async {
        let id = recording.meetingID
        guard !inFlight.contains(id), recording.state == .stopped || recording.state == .merged else { return }
        inFlight.insert(id)
        defer { inFlight.remove(id) }

        // Locking or backgrounding right after Stop must not cut the export.
        let background = BackgroundActivity(name: "phoebe.merge")
        defer { background.end() }

        var file: URL?
        if recording.state == .merged {
            let merged = RecordingPaths.directory(for: id, root: root).appending(path: recording.mergedFileName ?? RecordingPaths.mergedFileName)
            if FileManager.default.fileExists(atPath: merged.path) {
                file = merged
            } else {
                recording.state = .stopped
                recording.mergedFileName = nil
            }
        }
        if file == nil { file = await merge(recording) }
        guard let file else { return }
        await queue(recording, file: file)
    }

    func segmentsBytes(_ recording: LocalRecording) -> Int64 {
        let dir = RecordingPaths.directory(for: recording.meetingID, root: root)
        return recording.segments.reduce(0) { $0 + RecordingPaths.fileSize(dir.appending(path: $1.fileName)) }
    }

    func hasSpaceToMerge(_ recording: LocalRecording) -> Bool {
        storage.freeBytes() >= segmentsBytes(recording) + config.mergeHeadroomBytes
    }

    private func merge(_ recording: LocalRecording) async -> URL? {
        let id = recording.meetingID
        let dir = RecordingPaths.directory(for: id, root: root)
        let meeting = fetchMeeting(id)
        try? FileManager.default.removeItem(at: dir.appending(path: RecordingPaths.mergedTempFileName))
        let segments = recording.orderedSegments
            .map { dir.appending(path: $0.fileName) }
            .filter { FileManager.default.fileExists(atPath: $0.path) }
        guard !segments.isEmpty else {
            // Nothing was captured: no audio to merge or upload.
            try? FileManager.default.removeItem(at: dir)
            context.delete(recording)
            meeting?.localPhase = .none
            save()
            return nil
        }
        guard hasSpaceToMerge(recording) else {
            meeting?.localPhase = .waitingStorage
            save()
            return nil
        }
        meeting?.localPhase = .merging
        save()
        do {
            return try await merger.merge(segments: segments, into: dir)
        } catch {
            meeting?.localPhase = SegmentMerger.isOutOfSpace(error) || !hasSpaceToMerge(recording) ? .waitingStorage : .merging
            save()
            return nil
        }
    }

    private func queue(_ recording: LocalRecording, file: URL) async {
        let id = recording.meetingID
        let meeting = fetchMeeting(id)
        guard let sha = try? await Task.detached(operation: { try Checksum.sha256Hex(fileURL: file) }).value,
              let durationMs = await AudioFiles.playableDurationMs(file)
        else {
            meeting?.localPhase = .merging
            save()
            return
        }
        let size = Int(RecordingPaths.fileSize(file))
        recording.mergedFileName = file.lastPathComponent
        if fetchUpload(id) == nil {
            context.insert(PendingUpload(
                meetingID: id, ownerUserID: recording.ownerUserID,
                filePath: RecordingPaths.relativePath(of: file, base: base),
                sizeBytes: size, sha256: sha, contentType: "audio/mp4", durationMs: durationMs
            ))
        }
        if !hasTranscribeIntent(id) {
            context.insert(PendingIntent.transcribe(meetingID: id, language: meeting?.transcriptionLanguage ?? .auto))
        }
        recording.state = .queued
        recording.activeDurationMs = durationMs
        meeting?.localPhase = .queued
        meeting?.durationMs = durationMs
        save()
        onQueued(id)
    }

    private func fetchMeeting(_ id: UUID) -> Meeting? {
        try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
    }

    private func fetchUpload(_ id: UUID) -> PendingUpload? {
        try? context.fetch(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.meetingID == id })).first
    }

    private func hasTranscribeIntent(_ id: UUID) -> Bool {
        let raw = IntentType.transcribe.rawValue
        let count = (try? context.fetchCount(FetchDescriptor<PendingIntent>(predicate: #Predicate { $0.meetingID == id && $0.typeRaw == raw }))) ?? 0
        return count > 0
    }

    private func save() {
        try? context.save()
    }
}

@MainActor
final class BackgroundActivity {
    private var id: UIBackgroundTaskIdentifier = .invalid

    init(name: String) {
        id = UIApplication.shared.beginBackgroundTask(withName: name) { [weak self] in self?.end() }
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}
