import Foundation
import Testing
@testable import Phoebe

struct ImportValidatorTests {
    private let config = ServerConfig(
        maxAudioDurationMs: 3_000, maxUploadBytes: 200_000, uploadChunkMaxBytes: 33_554_432,
        supportedContentTypes: ["audio/mp4", "audio/mpeg", "audio/wav"], audioRetentionDays: 30, languages: ["auto", "id", "en"]
    )

    private func validator(free: Int64 = 10_000_000_000) -> ImportValidator {
        ImportValidator(config: config, storage: FakeStorage(free))
    }

    @Test func validM4AAndWAV() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let m4a = dir.appending(path: "rapat.m4a")
        let wav = dir.appending(path: "rapat.wav")
        try AudioFixtures.writeAAC(m4a, seconds: 2)
        try AudioFixtures.writeWAV(wav, seconds: 2)

        let a = try await validator().validate(m4a)
        #expect(a.kind == .m4a)
        #expect(a.kind.contentType == "audio/mp4")
        #expect(abs(a.durationMs - 2_000) <= 50)
        #expect(a.sizeBytes == RecordingPaths.fileSize(m4a))
        #expect(a.fileName == "rapat.m4a")

        let w = try await validator().validate(wav)
        #expect(w.kind == .wav)
        #expect(w.kind.contentType == "audio/wav")
        #expect(abs(w.durationMs - 2_000) <= 5)
    }

    @Test func wrongMagicBytesNamedM4AIsRejectedByContent() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fake = dir.appending(path: "notes.m4a")
        try Data("%PDF-1.7 this is a document, not audio".utf8).write(to: fake)
        await #expect(throws: ImportValidationError.type) { try await validator().validate(fake) }

        let wav = dir.appending(path: "source.wav")
        let wavAsM4A = dir.appending(path: "really-wav.m4a")
        try AudioFixtures.writeWAV(wav, seconds: 2)
        try FileManager.default.copyItem(at: wav, to: wavAsM4A)
        #expect(try await validator().validate(wavAsM4A).kind == .wav)
    }

    @Test func zeroLengthIsEmpty() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let empty = dir.appending(path: "empty.m4a")
        try Data().write(to: empty)
        await #expect(throws: ImportValidationError.empty) { try await validator().validate(empty) }
    }

    @Test func tooLarge() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let wav = dir.appending(path: "big.wav")
        try AudioFixtures.writeWAV(wav, seconds: 7)
        let size = RecordingPaths.fileSize(wav)
        await #expect(throws: ImportValidationError.tooBig(sizeBytes: size, limitBytes: 200_000)) { try await validator().validate(wav) }
    }

    @Test func tooLong() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let m4a = dir.appending(path: "long.m4a")
        try AudioFixtures.writeAAC(m4a, seconds: 4)
        do {
            _ = try await validator().validate(m4a)
            Issue.record("expected tooLong")
        } catch let error as ImportValidationError {
            guard case let .tooLong(duration, limit) = error else {
                Issue.record("got \(error)")
                return
            }
            #expect(limit == 3_000)
            #expect(abs(duration - 4_000) <= 50)
        }
    }

    @Test func shorterThanOneSecondIsEmpty() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let wav = dir.appending(path: "blip.wav")
        try AudioFixtures.writeWAV(wav, seconds: 0.5)
        await #expect(throws: ImportValidationError.empty) { try await validator().validate(wav) }
    }

    @Test func corruptContainerWithValidMagic() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let bad = dir.appending(path: "bad.m4a")
        var bytes = Data([0, 0, 0, 0x18]) + Data("ftypM4A ".utf8)
        bytes.append(Data(repeating: 0x42, count: 4_000))
        try bytes.write(to: bad)
        await #expect(throws: ImportValidationError.corrupt) { try await validator().validate(bad) }
    }

    @Test func insufficientStorage() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let m4a = dir.appending(path: "ok.m4a")
        try AudioFixtures.writeAAC(m4a, seconds: 2)
        await #expect(throws: ImportValidationError.storage) { try await validator(free: 10_000_000).validate(m4a) }
    }

    @Test func sniffRecognizesHeaders() {
        #expect(ImportValidator.sniff(Data([0, 0, 0, 0x20]) + Data("ftypisom".utf8)) == .m4a)
        #expect(ImportValidator.sniff(Data("ID3".utf8) + Data(repeating: 0, count: 9)) == .mp3)
        #expect(ImportValidator.sniff(Data([0xFF, 0xFB, 0x90, 0x64]) + Data(repeating: 0, count: 8)) == .mp3)
        #expect(ImportValidator.sniff(Data("RIFF".utf8) + Data([1, 2, 3, 4]) + Data("WAVE".utf8)) == .wav)
        #expect(ImportValidator.sniff(Data("RIFF".utf8) + Data([1, 2, 3, 4]) + Data("AVI ".utf8)) == nil)
        #expect(ImportValidator.sniff(Data()) == nil)
    }

    @Test func defaultsWithoutServerConfig() {
        let v = ImportValidator(config: nil, storage: FakeStorage())
        #expect(v.maxUploadBytes == 262_144_000)
        #expect(v.maxDurationMs == 7_200_000)
    }
}
