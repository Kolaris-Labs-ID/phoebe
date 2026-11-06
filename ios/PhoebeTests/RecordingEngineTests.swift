import AVFoundation
import Foundation
import SwiftData
import Testing
@testable import Phoebe

@MainActor
final class FakeCapture: AudioCapturing {
    var totalFrames: Int64 = 0
    var segmentFrames: Int64 = 0
    var level: Float = 0.5
    var writeFailed = false
    var onConfigurationChange: (@MainActor () -> Void)?

    var openURL: URL?
    var engineRunning = false
    var startCount = 0, restartCount = 0, rotateCount = 0, rebuildCount = 0
    var failStart = false

    func resetSession() { totalFrames = 0 }

    func openSegment(at url: URL) throws {
        openURL = url
        segmentFrames = 0
    }

    func rotateSegment(to url: URL) throws -> CapturedSegment? {
        rotateCount += 1
        let closed = finish()
        openURL = url
        return closed
    }

    func closeSegment() -> CapturedSegment? { finish() }

    func startEngine() async throws {
        startCount += 1
        if failStart { throw CaptureError.noFirstBuffer }
        engineRunning = true
        capture(frames: 1_600)
    }

    func stopEngine() { engineRunning = false }

    func restartEngine() throws {
        restartCount += 1
        engineRunning = true
    }

    func rebuildEngine() {
        rebuildCount += 1
        engineRunning = false
    }

    func capture(frames: Int64) {
        guard engineRunning, openURL != nil else { return }
        segmentFrames += frames
        totalFrames += frames
    }

    private func finish() -> CapturedSegment? {
        guard let url = openURL else { return nil }
        openURL = nil
        if segmentFrames > 0 { try? AudioFixtures.writeAAC(url, seconds: Double(segmentFrames) / 16_000) }
        let closed = CapturedSegment(url: url, frames: segmentFrames)
        segmentFrames = 0
        return closed
    }
}

@MainActor
final class FakeSession: AudioSessioning {
    var permission: MicPermission = .granted
    var currentInputName: String? = "iPhone Microphone"
    var activateCount = 0
    var failActivate = false
    var active = false

    func requestPermission() async -> Bool { permission == .granted }
    func activate() throws {
        activateCount += 1
        if failActivate { throw CaptureError.noInput }
        active = true
    }
    func deactivate() { active = false }
}

@MainActor
struct EngineHarness {
    let container: ModelContainer
    let context: ModelContext
    let capture = FakeCapture()
    let session = FakeSession()
    let storage: FakeStorage
    let root: URL
    let engine: RecordingEngine
    let events: Box<[AnalyticsEvent]>
    let queued: Box<[UUID]>
    let meetingID = UUID()

    init(free: Int64 = 10_000_000_000, limitMs: Int = 7_200_000) throws {
        container = try ModelContainerFactory.inMemory()
        context = container.mainContext
        storage = FakeStorage(free)
        root = try AudioFixtures.tempDir().appending(path: "recordings", directoryHint: .isDirectory)
        let events = Box<[AnalyticsEvent]>([])
        let queued = Box<[UUID]>([])
        self.events = events
        self.queued = queued
        var config = RecordingConfig.standard
        config.segmentSeconds = 1
        engine = RecordingEngine(
            context: context, userID: UUID(), capture: capture, session: session, storage: storage,
            config: config, root: root, limitMs: { limitMs },
            logEvent: { e, _ in events.mutate { $0.append(e) } },
            onQueued: { id in queued.mutate { $0.append(id) } },
            tickInterval: nil
        )
        let meeting = Meeting(id: meetingID, source: .recording, title: "Weekly", language: .id)
        context.insert(meeting)
        try context.save()
    }

    func recording() -> LocalRecording? {
        let id = meetingID
        return try? context.fetch(FetchDescriptor<LocalRecording>(predicate: #Predicate { $0.meetingID == id })).first
    }

    func savedSegments() -> [RecordingSegment] {
        recording()?.orderedSegments ?? []
    }

    func meeting() -> Meeting? {
        let id = meetingID
        return try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
    }

    func cleanup() {
        try? FileManager.default.removeItem(at: root.deletingLastPathComponent())
    }
}

struct SegmentWriterTests {
    @Test func convertsHardwareRateToAACAndRotatesWithoutLosingFrames() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let writer = SegmentWriter(config: .standard)
        let input = AVAudioFormat(standardFormatWithSampleRate: 48_000, channels: 1)!
        var phase: Float = 0
        func feed(seconds: Double) {
            for _ in 0..<Int(seconds * 10) {
                let buffer = AVAudioPCMBuffer(pcmFormat: input, frameCapacity: 4_800)!
                buffer.frameLength = 4_800
                for i in 0..<4_800 {
                    buffer.floatChannelData![0][i] = 0.3 * sinf(phase)
                    phase += 2 * .pi * 440 / 48_000
                }
                writer.append(buffer)
            }
        }

        try writer.open(dir.appending(path: "seg-0001.m4a"))
        feed(seconds: 2)
        let first = try #require(try writer.rotate(to: dir.appending(path: "seg-0002.m4a")))
        feed(seconds: 1)
        let second = try #require(writer.close())

        #expect(abs(first.frames - 32_000) <= 512)
        #expect(abs(second.frames - 16_000) <= 512)
        #expect(writer.stats.totalFrames == first.frames + second.frames)
        #expect(writer.stats.level > 0)
        #expect(!writer.stats.failed)
        #expect(abs(try await AudioFixtures.durationMs(first.url) - 2_000) <= 100)
        #expect(abs(try await AudioFixtures.durationMs(second.url) - 1_000) <= 100)
        #expect(writer.close() == nil)
    }
}

@MainActor
struct RecordingEngineTests {
    @Test func startSucceedsOnlyAfterFirstBuffer() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.prepare()
        #expect(h.engine.phase == .ready)
        await h.engine.start(meetingID: h.meetingID)
        #expect(h.engine.phase == .recording)
        #expect(h.session.activateCount == 1)
        #expect(h.capture.startCount == 1)
        #expect(h.recording()?.state == .recording)
        #expect(h.meeting()?.localPhase == .recording)
        #expect(h.events.value == [.recordingStarted])
    }

    @Test func startFailureKeepsStartEnabled() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        h.capture.failStart = true
        await h.engine.prepare()
        await h.engine.start(meetingID: h.meetingID)
        #expect(h.engine.phase == .ready)
        #expect(h.engine.phase.buttonState == .ready)
        #expect(h.engine.banner == .startFailed)
        #expect(!h.session.active)
        #expect(h.recording() == nil)

        h.capture.failStart = false
        await h.engine.start(meetingID: h.meetingID)
        #expect(h.engine.phase == .recording)
        #expect(h.engine.banner == nil)
    }

    @Test func sessionActivationFailureKeepsReady() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        h.session.failActivate = true
        await h.engine.start(meetingID: h.meetingID)
        #expect(h.engine.phase == .ready)
        #expect(h.engine.banner == .startFailed)
        #expect(h.capture.startCount == 0)
    }

    @Test func storagePreflightBlocksStart() async throws {
        let h = try EngineHarness(free: 200_000_000)
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        #expect(h.engine.phase == .ready)
        #expect(h.engine.banner == .storagePreflight(requiredBytes: 300_000_000))
        #expect(h.capture.startCount == 0)
    }

    @Test func rotatesAtSegmentLengthWithoutEngineRestartAndSavesManifest() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 14_400)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.capture.rotateCount == 1)
        #expect(h.savedSegments().map(\.fileName) == ["seg-0001.m4a"])
        #expect(abs(h.savedSegments()[0].durationMs - 1_000) <= 50)

        h.capture.capture(frames: 8_000)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.capture.rotateCount == 1)

        h.capture.capture(frames: 8_000)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.capture.rotateCount == 2)
        #expect(h.capture.startCount == 1)
        #expect(h.capture.restartCount == 0)
        #expect(h.savedSegments().map(\.index) == [1, 2])
        #expect(h.engine.segmentCount == 2)
        #expect(h.capture.openURL?.lastPathComponent == "seg-0003.m4a")
    }

    @Test func pauseSavesSegmentAndExcludesPausedTime() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 6_400)
        h.engine.pause()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .paused)
        #expect(h.recording()?.state == .paused)
        #expect(h.savedSegments().count == 1)
        #expect(h.engine.activeDurationMs == 500)

        h.capture.capture(frames: 16_000)
        #expect(h.engine.activeDurationMs == 500)

        await h.engine.resume()
        #expect(h.engine.phase == .recording)
        h.engine.tick()
        #expect(h.engine.activeDurationMs == 600)
    }

    @Test func stopMergesAndQueuesUploadWithTranscribeIntent() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 14_400)
        h.engine.tick()
        await h.engine.waitForIdle()
        h.capture.capture(frames: 8_000)
        let summary = await h.engine.stop()

        #expect(h.engine.phase == .stopped)
        #expect(summary?.segmentCount == 2)
        #expect(summary?.durationMs == 1_500)
        let recording = try #require(h.recording())
        #expect(recording.state == .queued)
        #expect(recording.mergedFileName == "audio.m4a")
        let id = h.meetingID
        let upload = try #require(try h.context.fetch(FetchDescriptor<PendingUpload>(predicate: #Predicate { $0.meetingID == id })).first)
        let base = h.root.deletingLastPathComponent()
        let merged = RecordingPaths.resolve(upload.filePath, base: base)
        #expect(upload.filePath == "recordings/\(id.lower)/audio.m4a")
        #expect(upload.contentType == "audio/mp4")
        #expect(upload.sha256 == (try Checksum.sha256Hex(fileURL: merged)))
        #expect(upload.sizeBytes == Int(RecordingPaths.fileSize(merged)))
        #expect(abs(upload.durationMs - 1_500) <= 100)
        let intents = try h.context.fetch(FetchDescriptor<PendingIntent>(predicate: #Predicate { $0.meetingID == id }))
        #expect(intents.count == 1)
        #expect(intents.first?.type == .transcribe)
        #expect(intents.first?.body == APICoding.encode(TranscriptionRequest(language: .id)))
        #expect(h.meeting()?.localPhase == .queued)
        #expect(h.queued.value == [id])
        #expect(!h.session.active)
    }

    @Test func routeChangeReinstallsTapOnNewInput() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 4_000)
        h.session.currentInputName = "Headset Microphone"
        h.engine.handleRouteChange(reason: .oldDeviceUnavailable)
        await h.engine.waitForIdle()
        #expect(h.capture.restartCount == 1)
        #expect(h.capture.startCount == 1)
        #expect(h.engine.phase == .recording)
        #expect(h.engine.banner == .routeChanged(inputName: "Headset Microphone"))
        #expect(h.savedSegments().count == 1)
        #expect(h.capture.openURL?.lastPathComponent == "seg-0002.m4a")

        h.capture.capture(frames: 1_000)
        h.capture.onConfigurationChange?()
        await h.engine.waitForIdle()
        #expect(h.capture.restartCount == 2)
        #expect(h.savedSegments().count == 2)
    }

    @Test func routeLossWithoutInputInterruptsUntilInputReturns() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 4_000)
        h.session.currentInputName = nil
        h.engine.handleRouteChange(reason: .oldDeviceUnavailable)
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .interrupted)
        #expect(h.engine.banner == .noInput)
        #expect(!h.engine.canResume)
        #expect(h.savedSegments().count == 1)

        h.session.currentInputName = "iPhone Microphone"
        h.engine.handleRouteChange(reason: .newDeviceAvailable)
        await h.engine.waitForIdle()
        #expect(h.engine.canResume)
        #expect(h.engine.phase == .interrupted)
    }

    @Test func interruptionClosesSegmentAndNeverAutoResumes() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 8_000)
        h.engine.handleInterruption(began: true, shouldResume: false)
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .interrupted)
        #expect(h.engine.banner == .interrupted(savedMs: 600))
        #expect(h.recording()?.state == .interrupted)
        #expect(h.savedSegments().count == 1)
        #expect(h.events.value == [.recordingStarted, .recordingInterrupted])

        h.engine.handleInterruption(began: false, shouldResume: true)
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .interrupted)
        #expect(h.engine.canResume)
        #expect(h.capture.startCount == 1)
        #expect(!h.session.active)

        await h.engine.resume()
        #expect(h.engine.phase == .recording)
        #expect(h.capture.startCount == 2)
    }

    @Test func writeFailureInterrupts() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 4_000)
        h.capture.writeFailed = true
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .interrupted)
        #expect(h.engine.banner == .writeFailed)
        #expect(h.engine.canResume)
        #expect(h.savedSegments().count == 1)
    }

    @Test func mediaResetRebuildsAndInterrupts() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 4_000)
        h.engine.handleMediaServicesReset()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .interrupted)
        #expect(h.engine.banner == .mediaReset)
        #expect(h.capture.rebuildCount == 1)
        #expect(h.savedSegments().count == 1)
        #expect(h.engine.canResume)
    }

    @Test func storageCriticalStopsSafelyAndMergeWaitsForStorage() async throws {
        let h = try EngineHarness()
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.capture.capture(frames: 8_000)
        h.storage.free = 250_000_000
        h.engine.checkStorage()
        await h.engine.waitForIdle()
        #expect(h.engine.banner == .storageLow(freeBytes: 250_000_000))
        #expect(h.engine.phase == .recording)

        h.storage.free = 40_000_000
        h.engine.checkStorage()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .stopped)
        #expect(h.engine.banner == .storageStopped(savedMs: 600))
        #expect(h.recording()?.state == .stopped)
        #expect(h.savedSegments().count == 1)
        #expect(h.meeting()?.localPhase == .waitingStorage)

        h.storage.free = 1_000_000_000
        await h.engine.retryPendingMerges()
        #expect(h.recording()?.state == .queued)
        #expect(h.meeting()?.localPhase == .queued)
    }

    @Test func durationLimitWarnsThenStops() async throws {
        let h = try EngineHarness(limitMs: 60_000)
        defer { h.cleanup() }
        await h.engine.start(meetingID: h.meetingID)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.engine.banner == .limitNear(limitMinutes: 1, minutesLeft: 1))

        h.capture.capture(frames: 878_399)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .recording)

        h.capture.capture(frames: 1)
        h.engine.tick()
        await h.engine.waitForIdle()
        #expect(h.engine.phase == .stopped)
        #expect(h.engine.banner == .limitReached(limitMinutes: 1))
        #expect(h.recording()?.state == .queued)
    }
}
