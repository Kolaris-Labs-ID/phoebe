import Foundation
import SwiftData
import Testing
@testable import Phoebe

@MainActor
struct RecordingRecoveryTests {
    @MainActor
    struct Env {
        let container: ModelContainer
        let context: ModelContext
        let root: URL
        let queued: Box<[UUID]>
        let finalizer: RecordingFinalizer
        let recovery: RecordingRecovery

        init(free: Int64 = 10_000_000_000) throws {
            container = try ModelContainerFactory.inMemory()
            context = container.mainContext
            root = try AudioFixtures.tempDir().appending(path: "recordings", directoryHint: .isDirectory)
            let queued = Box<[UUID]>([])
            self.queued = queued
            finalizer = RecordingFinalizer(context: context, root: root, storage: FakeStorage(free), onQueued: { id in queued.mutate { $0.append(id) } })
            recovery = RecordingRecovery(context: context, root: root, finalizer: finalizer)
        }

        func makeRecording(state: LocalRecordingState, language: TranscriptionLanguage = .en) throws -> (LocalRecording, URL) {
            let id = UUID()
            context.insert(Meeting(id: id, source: .recording, title: "Sync", language: language))
            let recording = LocalRecording(meetingID: id, ownerUserID: UUID(), state: state)
            context.insert(recording)
            try context.save()
            return (recording, try RecordingPaths.prepareDirectory(for: id, root: root))
        }

        func addSegment(_ recording: LocalRecording, dir: URL, index: Int, seconds: Double, listed: Bool = true) throws {
            let name = RecordingSegment.fileName(index: index)
            try AudioFixtures.writeAAC(dir.appending(path: name), seconds: seconds)
            guard listed else { return }
            let segment = RecordingSegment(index: index, fileName: name, durationMs: Int(seconds * 1000), bytes: 0)
            context.insert(segment)
            segment.recording = recording
            try context.save()
        }

        func upload(_ id: UUID) -> PendingUpload? {
            try? context.fetch(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.meetingID == id })).first
        }

        func intents(_ id: UUID) -> [PendingIntent] {
            (try? context.fetch(FetchDescriptor<PendingIntent>(predicate: #Predicate { $0.meetingID == id }))) ?? []
        }

        func cleanup() { try? FileManager.default.removeItem(at: root.deletingLastPathComponent()) }
    }

    @Test func uncleanEndRecoversValidSegmentsDropsUnreadableAndIncludesUnlisted() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, dir) = try env.makeRecording(state: .recording)
        try env.addSegment(recording, dir: dir, index: 1, seconds: 1)
        try env.addSegment(recording, dir: dir, index: 2, seconds: 2, listed: false)
        let truncated = dir.appending(path: "seg-0003.m4a")
        try AudioFixtures.writeUnfinalizedAAC(truncated, seconds: 1)
        let listed = RecordingSegment(index: 3, fileName: "seg-0003.m4a", durationMs: 1_000, bytes: 0)
        env.context.insert(listed)
        listed.recording = recording
        try env.context.save()
        #expect(await AudioFiles.playableDurationMs(truncated) == nil)

        let pending = await env.recovery.run()

        #expect(pending?.meetingID == recording.meetingID)
        #expect(recording.orderedSegments.map(\.fileName) == ["seg-0001.m4a", "seg-0002.m4a"])
        #expect(!FileManager.default.fileExists(atPath: truncated.path))
        #expect(abs(recording.activeDurationMs - 3_000) <= 100)
        #expect(recording.state == .recording)
        #expect(env.upload(recording.meetingID) == nil)
        #expect(env.queued.value.isEmpty)
    }

    @Test func recoverThenMergesAndQueues() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, dir) = try env.makeRecording(state: .interrupted)
        try env.addSegment(recording, dir: dir, index: 1, seconds: 1)
        _ = await env.recovery.run()

        recording.state = .stopped
        await env.finalizer.finalize(recording)

        #expect(recording.state == .queued)
        #expect(env.upload(recording.meetingID) != nil)
        #expect(env.intents(recording.meetingID).count == 1)
    }

    @Test func noValidSegmentsRemovesRecordingWithoutPrompt() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, dir) = try env.makeRecording(state: .paused)
        let id = recording.meetingID
        try Data("not audio".utf8).write(to: dir.appending(path: "seg-0001.m4a"))

        #expect(await env.recovery.run() == nil)
        let left = try env.context.fetch(FetchDescriptor<LocalRecording>(predicate: #Predicate { $0.meetingID == id }))
        #expect(left.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: dir.path))
    }

    @Test func stoppedWithPartialTempDeletesTempAndRerunsMerge() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, dir) = try env.makeRecording(state: .stopped)
        try env.addSegment(recording, dir: dir, index: 1, seconds: 1)
        try env.addSegment(recording, dir: dir, index: 2, seconds: 1)
        let tmp = dir.appending(path: "audio.m4a.tmp")
        try Data(repeating: 7, count: 1_000).write(to: tmp)

        let pending = await env.recovery.run()

        #expect(pending == nil)
        #expect(!FileManager.default.fileExists(atPath: tmp.path))
        let merged = dir.appending(path: "audio.m4a")
        #expect(abs(try await AudioFixtures.durationMs(merged) - 2_000) <= 100)
        #expect(recording.state == .queued)
        #expect(recording.mergedFileName == "audio.m4a")
        let upload = try #require(env.upload(recording.meetingID))
        #expect(upload.sha256 == (try Checksum.sha256Hex(fileURL: merged)))
        #expect(env.queued.value == [recording.meetingID])
    }

    @Test func mergedWithoutPendingUploadCreatesUploadAndIntent() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, dir) = try env.makeRecording(state: .merged, language: .en)
        let merged = dir.appending(path: "audio.m4a")
        try AudioFixtures.writeAAC(merged, seconds: 1.5)
        recording.mergedFileName = "audio.m4a"
        try env.context.save()

        _ = await env.recovery.run()

        #expect(recording.state == .queued)
        let upload = try #require(env.upload(recording.meetingID))
        #expect(upload.sha256 == (try Checksum.sha256Hex(fileURL: merged)))
        #expect(upload.sizeBytes == Int(RecordingPaths.fileSize(merged)))
        #expect(upload.fileURL.lastPathComponent == "audio.m4a")
        let intents = env.intents(recording.meetingID)
        #expect(intents.count == 1)
        #expect(intents.first?.body == APICoding.encode(TranscriptionRequest(language: .en)))
    }

    @Test func queuedRecordingsAreLeftAlone() async throws {
        let env = try Env()
        defer { env.cleanup() }
        let (recording, _) = try env.makeRecording(state: .queued)
        #expect(await env.recovery.run() == nil)
        #expect(recording.state == .queued)
        #expect(env.upload(recording.meetingID) == nil)
    }
}
