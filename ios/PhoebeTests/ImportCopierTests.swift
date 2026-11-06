import CryptoKit
import Foundation
import Testing
@testable import Phoebe

struct ImportCopierTests {
    @Test func streamedCopyIsByteEqualWithIncrementalDigestAndMonotonicProgress() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let count = 5 * 1024 * 1024 + 321
        var data = Data(count: count)
        data.withUnsafeMutableBytes { arc4random_buf($0.baseAddress, count) }
        let source = dir.appending(path: "picked.mp3")
        try data.write(to: source)
        let root = dir.appending(path: "recordings", directoryHint: .isDirectory)
        let meetingID = UUID()
        let progress = Box<[Double]>([])

        let result = try await ImportCopier.copy(
            from: source, kind: .mp3, sizeBytes: Int64(count), meetingID: meetingID, root: root, chunkSize: 256 * 1024,
            progress: { value in progress.mutate { $0.append(value) } }
        )

        #expect(try Data(contentsOf: result.url) == data)
        #expect(result.url.lastPathComponent == "import.mp3")
        #expect(result.relativePath == "recordings/\(meetingID.lower)/import.mp3")
        #expect(RecordingPaths.resolve(result.relativePath, base: dir).standardizedFileURL == result.url.standardizedFileURL)
        #expect(result.sizeBytes == count)
        let oneShot = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        #expect(result.sha256 == oneShot)
        #expect(result.sha256 == Checksum.sha256Hex(data))

        let values = progress.value
        #expect(values.count > 20)
        #expect(zip(values, values.dropFirst()).allSatisfy { $0 <= $1 })
        #expect(values.first == 0)
        #expect(values.last == 1.0)
        #expect(!FileManager.default.fileExists(atPath: result.url.path + ".tmp"))
    }

    @Test func missingSourceLeavesNoPartialFile() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let root = dir.appending(path: "recordings", directoryHint: .isDirectory)
        let meetingID = UUID()
        await #expect(throws: (any Error).self) {
            try await ImportCopier.copy(from: dir.appending(path: "gone.wav"), kind: .wav, sizeBytes: 10, meetingID: meetingID, root: root)
        }
        let meetingDir = RecordingPaths.directory(for: meetingID, root: root)
        #expect(((try? FileManager.default.contentsOfDirectory(atPath: meetingDir.path)) ?? []).isEmpty)
    }
}
