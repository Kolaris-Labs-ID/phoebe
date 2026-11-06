import Foundation
import SwiftData

// Launch reconciliation (ios.md §6). Never starts the microphone.
@MainActor
struct RecordingRecovery {
    let context: ModelContext
    let root: URL
    let finalizer: RecordingFinalizer

    func run(excluding liveMeetingID: UUID? = nil) async -> LocalRecording? {
        let queued = LocalRecordingState.queued.rawValue
        let recordings = (try? context.fetch(FetchDescriptor<LocalRecording>(predicate: #Predicate { $0.stateRaw != queued }))) ?? []
        var unfinished: [LocalRecording] = []
        for recording in recordings where recording.meetingID != liveMeetingID {
            switch recording.state {
            case .recording, .paused, .interrupted:
                if await reconcile(recording) { unfinished.append(recording) }
            case .stopped, .merged:
                await finalizer.finalize(recording)
            case .queued:
                break
            }
        }
        return unfinished.max { $0.startedAt < $1.startedAt }
    }

    // Validates every seg-*.m4a on disk against the manifest: unlisted valid files are added,
    // unreadable ones (the in-progress segment at a crash has no `moov`) are dropped.
    func reconcile(_ recording: LocalRecording) async -> Bool {
        let fm = FileManager.default
        let dir = RecordingPaths.directory(for: recording.meetingID, root: root)
        let names = ((try? fm.contentsOfDirectory(atPath: dir.path)) ?? []).filter { RecordingPaths.segmentIndex(fileName: $0) != nil }
        var valid: [String: Int] = [:]
        for name in names {
            let url = dir.appending(path: name)
            if let ms = await AudioFiles.playableDurationMs(url) {
                valid[name] = ms
            } else {
                try? fm.removeItem(at: url)
            }
        }

        var listed = Set<String>()
        for segment in recording.segments {
            if let ms = valid[segment.fileName] {
                segment.durationMs = ms
                listed.insert(segment.fileName)
            } else {
                context.delete(segment)
            }
        }
        for (name, ms) in valid where !listed.contains(name) {
            let segment = RecordingSegment(
                index: RecordingPaths.segmentIndex(fileName: name) ?? 0, fileName: name, durationMs: ms,
                bytes: Int(RecordingPaths.fileSize(dir.appending(path: name)))
            )
            context.insert(segment)
            segment.recording = recording
        }
        recording.activeDurationMs = valid.values.reduce(0, +)

        guard !valid.isEmpty else {
            let id = recording.meetingID
            try? fm.removeItem(at: dir)
            context.delete(recording)
            if let meeting = try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first {
                meeting.localPhase = .none
            }
            try? context.save()
            return false
        }
        try? context.save()
        return true
    }
}
