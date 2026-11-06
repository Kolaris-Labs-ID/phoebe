import AVFoundation
import Foundation
import Testing
@testable import Phoebe

struct SegmentMergerTests {
    @Test func threeSegmentsMergeToSumDuration() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let lengths = [1.0, 2.0, 1.5]
        var urls: [URL] = []
        for (i, seconds) in lengths.enumerated() {
            let url = dir.appending(path: RecordingSegment.fileName(index: i + 1))
            try AudioFixtures.writeAAC(url, seconds: seconds)
            urls.append(url)
        }
        var sum = 0
        for url in urls { sum += try await AudioFixtures.durationMs(url) }

        let merged = try await SegmentMerger().merge(segments: urls, into: dir)

        #expect(merged.lastPathComponent == "audio.m4a")
        #expect(abs(try await AudioFixtures.durationMs(merged) - sum) <= 50)
        #expect(await AudioFiles.playableDurationMs(merged) != nil)
        #expect(!FileManager.default.fileExists(atPath: dir.appending(path: "audio.m4a.tmp").path))
    }

    @Test func sixtyTwoMinuteSegmentsStayWithinOneSecondOfSum() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let first = dir.appending(path: RecordingSegment.fileName(index: 1))
        try AudioFixtures.writeAAC(first, seconds: 120)
        var urls = [first]
        for i in 2...60 {
            let url = dir.appending(path: RecordingSegment.fileName(index: i))
            try FileManager.default.copyItem(at: first, to: url)
            urls.append(url)
        }
        let sum = try await AudioFixtures.durationMs(first) * 60

        let merged = try await SegmentMerger().merge(segments: urls, into: dir)
        let duration = try await AudioFixtures.durationMs(merged)

        #expect(duration <= sum + 1_000)
        #expect(duration >= sum - 1_000)
        #expect(Double(RecordingPaths.fileSize(merged)) <= Double(RecordingPaths.fileSize(first) * 60) * 1.05)
        #expect(RecordingPaths.fileSize(merged) < 24_000_000)
    }

    @Test func outputAppearsOnlyAfterRename() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let seg = dir.appending(path: RecordingSegment.fileName(index: 1))
        try AudioFixtures.writeAAC(seg, seconds: 1)
        let observed = Box<(tmpExists: Bool, finalExists: Bool)?>(nil)

        let merged = try await SegmentMerger().merge(segments: [seg], into: dir) { tmp, final in
            observed.value = (FileManager.default.fileExists(atPath: tmp.path), FileManager.default.fileExists(atPath: final.path))
        }

        #expect(observed.value?.tmpExists == true)
        #expect(observed.value?.finalExists == false)
        #expect(FileManager.default.fileExists(atPath: merged.path))
        #expect(!FileManager.default.fileExists(atPath: dir.appending(path: "audio.m4a.tmp").path))
    }

    @Test func emptySegmentListThrows() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        await #expect(throws: MergeError.self) { try await SegmentMerger().merge(segments: [], into: dir) }
    }
}
