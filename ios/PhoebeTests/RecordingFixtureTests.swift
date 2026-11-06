import AVFoundation
import Foundation
import SwiftData
import Testing
@testable import Phoebe

enum AudioFixtures {
    static func tempDir() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appending(path: "phoebe-audio-\(UUID().uuidString)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    static func writeAAC(_ url: URL, seconds: Double, sampleRate: Double = 16_000) throws {
        try write(url, seconds: seconds, sampleRate: sampleRate, settings: [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: sampleRate,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 24_000,
        ])
    }

    static func writeWAV(_ url: URL, seconds: Double, sampleRate: Double = 16_000) throws {
        try write(url, seconds: seconds, sampleRate: sampleRate, settings: [
            AVFormatIDKey: kAudioFormatLinearPCM,
            AVSampleRateKey: sampleRate,
            AVNumberOfChannelsKey: 1,
            AVLinearPCMBitDepthKey: 16,
            AVLinearPCMIsFloatKey: false,
            AVLinearPCMIsBigEndianKey: false,
        ])
    }

    private static func write(_ url: URL, seconds: Double, sampleRate: Double, settings: [String: Any]) throws {
        let file = try AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
        let format = file.processingFormat
        let total = AVAudioFrameCount(seconds * sampleRate)
        let chunk: AVAudioFrameCount = 16_000
        var written: AVAudioFrameCount = 0
        while written < total {
            let n = min(chunk, total - written)
            let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: n)!
            buffer.frameLength = n
            let samples = buffer.floatChannelData![0]
            for i in 0..<Int(n) {
                samples[i] = 0.3 * sinf(2 * .pi * 440 * Float(Int(written) + i) / Float(sampleRate))
            }
            try file.write(from: buffer)
            written += n
        }
        if #available(iOS 18.0, *) { file.close() }
    }

    // Copies the bytes on disk while the writer is still open: the state of the in-progress
    // segment when the app is killed (no finalized `moov`).
    static func writeUnfinalizedAAC(_ url: URL, seconds: Double) throws {
        let scratch = url.deletingLastPathComponent().appending(path: "open-\(UUID().uuidString).m4a")
        let file = try AVAudioFile(forWriting: scratch, settings: [
            AVFormatIDKey: kAudioFormatMPEG4AAC, AVSampleRateKey: 16_000, AVNumberOfChannelsKey: 1, AVEncoderBitRateKey: 24_000,
        ], commonFormat: .pcmFormatFloat32, interleaved: false)
        let n = AVAudioFrameCount(seconds * 16_000)
        let buffer = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: n)!
        buffer.frameLength = n
        for i in 0..<Int(n) { buffer.floatChannelData![0][i] = 0.3 * sinf(Float(i) * 0.17) }
        try file.write(from: buffer)
        try FileManager.default.copyItem(at: scratch, to: url)
        if #available(iOS 18.0, *) { file.close() }
        try FileManager.default.removeItem(at: scratch)
    }

    static func durationMs(_ url: URL) async throws -> Int {
        let duration = try await AVURLAsset(url: url).load(.duration)
        return Int((duration.seconds * 1000).rounded())
    }
}

final class Box<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var _value: T
    init(_ value: T) { _value = value }
    var value: T {
        get { lock.withLock { _value } }
        set { lock.withLock { _value = newValue } }
    }
    func mutate(_ body: (inout T) -> Void) { lock.withLock { body(&_value) } }
}

final class FakeStorage: StorageProviding, @unchecked Sendable {
    private let box: Box<Int64>
    init(_ free: Int64 = 10_000_000_000) { box = Box(free) }
    var free: Int64 {
        get { box.value }
        set { box.value = newValue }
    }
    func freeBytes() -> Int64 { box.value }
}

struct AudioFixtureTests {
    @Test func fixturesProducePlayableFiles() async throws {
        let dir = try AudioFixtures.tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let aac = dir.appending(path: "a.m4a")
        let wav = dir.appending(path: "a.wav")
        try AudioFixtures.writeAAC(aac, seconds: 1)
        try AudioFixtures.writeWAV(wav, seconds: 1)
        #expect(abs(try await AudioFixtures.durationMs(aac) - 1000) <= 50)
        #expect(abs(try await AudioFixtures.durationMs(wav) - 1000) <= 5)
        #expect(await AudioFiles.playableDurationMs(aac) != nil)
    }
}
