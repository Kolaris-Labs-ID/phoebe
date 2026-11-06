import AVFoundation
import os

struct CapturedSegment: Equatable, Sendable {
    let url: URL
    let frames: Int64
}

enum CaptureError: Error {
    case noInput, noFirstBuffer, cannotOpenFile
}

@MainActor
protocol AudioCapturing: AnyObject {
    var totalFrames: Int64 { get }
    var segmentFrames: Int64 { get }
    var level: Float { get }
    var writeFailed: Bool { get }
    var onConfigurationChange: (@MainActor () -> Void)? { get set }

    func resetSession()
    func openSegment(at url: URL) throws
    func rotateSegment(to url: URL) throws -> CapturedSegment?
    func closeSegment() -> CapturedSegment?
    func startEngine() async throws
    func stopEngine()
    func restartEngine() throws
    func rebuildEngine()
}

// One continuous input tap for the whole session; rotation only swaps the AVAudioFile on the
// writer queue, so the engine is never restarted from the background (ios.md §6).
@MainActor
final class CaptureEngine: AudioCapturing {
    private var engine = AVAudioEngine()
    private let writer: SegmentWriter
    private let config: RecordingConfig
    private var tapInstalled = false
    private var configObserver: NSObjectProtocol?
    var onConfigurationChange: (@MainActor () -> Void)?

    init(config: RecordingConfig = .standard) {
        self.config = config
        self.writer = SegmentWriter(config: config)
        observeConfiguration()
    }

    var totalFrames: Int64 { writer.stats.totalFrames }
    var segmentFrames: Int64 { writer.stats.segmentFrames }
    var level: Float { writer.stats.level }
    var writeFailed: Bool { writer.stats.failed }

    func resetSession() { writer.resetTotals() }

    func openSegment(at url: URL) throws { try writer.open(url) }

    func rotateSegment(to url: URL) throws -> CapturedSegment? { try writer.rotate(to: url) }

    func closeSegment() -> CapturedSegment? { writer.close() }

    func startEngine() async throws {
        try installTapAndStart()
        let deadline = ContinuousClock.now + config.firstBufferTimeout
        while writer.stats.segmentFrames == 0 {
            if ContinuousClock.now >= deadline || writer.stats.failed {
                stopEngine()
                throw CaptureError.noFirstBuffer
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    func stopEngine() {
        engine.stop()
        if tapInstalled {
            engine.inputNode.removeTap(onBus: 0)
            tapInstalled = false
        }
    }

    func restartEngine() throws {
        stopEngine()
        try installTapAndStart()
    }

    func rebuildEngine() {
        stopEngine()
        if let configObserver { NotificationCenter.default.removeObserver(configObserver) }
        engine = AVAudioEngine()
        observeConfiguration()
    }

    private func installTapAndStart() throws {
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else { throw CaptureError.noInput }
        input.installTap(onBus: 0, bufferSize: 4096, format: format, block: Self.tapBlock(writer))
        tapInstalled = true
        engine.prepare()
        do {
            try engine.start()
        } catch {
            stopEngine()
            throw error
        }
    }

    // Built outside the main actor so the block is not MainActor-isolated: it runs on the audio thread.
    private nonisolated static func tapBlock(_ writer: SegmentWriter) -> AVAudioNodeTapBlock {
        { buffer, _ in writer.append(buffer) }
    }

    private func observeConfiguration() {
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.onConfigurationChange?() }
        }
    }
}

final class SegmentWriter: @unchecked Sendable {
    struct Stats: Sendable {
        var totalFrames: Int64 = 0
        var segmentFrames: Int64 = 0
        var level: Float = 0
        var failed = false
    }

    private struct Unchecked<T>: @unchecked Sendable { let value: T }
    private final class Flag: @unchecked Sendable { var consumed = false }

    private let queue = DispatchQueue(label: "com.kolarislabs.phoebe.segment-writer", qos: .userInitiated)
    private let lock = OSAllocatedUnfairLock(initialState: Stats())
    private let settings: [String: Any]
    private let outputFormat: AVAudioFormat
    private var file: AVAudioFile?
    private var url: URL?
    private var converter: AVAudioConverter?

    init(config: RecordingConfig) {
        settings = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: config.sampleRate,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: config.bitRate,
        ]
        outputFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: config.sampleRate, channels: 1, interleaved: false)!
    }

    var stats: Stats { lock.withLock { $0 } }

    func resetTotals() {
        queue.sync { lock.withLock { $0 = Stats() } }
    }

    func open(_ url: URL) throws {
        try queue.sync {
            let next = try makeFile(url)
            finalizeCurrent()
            file = next
            self.url = url
            converter = nil
            lock.withLock { $0.segmentFrames = 0; $0.failed = false }
        }
    }

    // The next file is created before the current one is released, so a failure leaves the
    // current segment recording and no buffer falls between the two files.
    func rotate(to url: URL) throws -> CapturedSegment? {
        try queue.sync {
            let next = try makeFile(url)
            let closed = finalizeCurrent()
            file = next
            self.url = url
            lock.withLock { $0.segmentFrames = 0 }
            return closed
        }
    }

    func close() -> CapturedSegment? {
        queue.sync {
            let closed = finalizeCurrent()
            converter = nil
            return closed
        }
    }

    func append(_ buffer: AVAudioPCMBuffer) {
        guard let copy = Self.copy(buffer) else { return }
        let level = Self.level(of: buffer)
        lock.withLock { $0.level = level }
        let boxed = Unchecked(value: copy)
        queue.async { self.write(boxed.value) }
    }

    private func makeFile(_ url: URL) throws -> AVAudioFile {
        try AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
    }

    @discardableResult
    private func finalizeCurrent() -> CapturedSegment? {
        guard let current = file, let url else { return nil }
        // iOS 18 finalizes the `moov` atom on close(); iOS 17 does it when the file deallocates.
        if #available(iOS 18.0, *) { current.close() }
        file = nil
        self.url = nil
        let frames = lock.withLock { $0.segmentFrames }
        return CapturedSegment(url: url, frames: frames)
    }

    private func write(_ input: AVAudioPCMBuffer) {
        guard let file, !lock.withLock({ $0.failed }) else { return }
        if converter == nil || converter?.inputFormat != input.format {
            converter = AVAudioConverter(from: input.format, to: outputFormat)
        }
        guard let converter else { return markFailed() }
        let ratio = outputFormat.sampleRate / input.format.sampleRate
        let capacity = AVAudioFrameCount((Double(input.frameLength) * ratio).rounded(.up)) + 64
        guard let output = AVAudioPCMBuffer(pcmFormat: outputFormat, frameCapacity: capacity) else { return markFailed() }
        let flag = Flag()
        let source = Unchecked(value: input)
        var error: NSError?
        let status = converter.convert(to: output, error: &error) { _, outStatus in
            if flag.consumed {
                outStatus.pointee = .noDataNow
                return nil
            }
            flag.consumed = true
            outStatus.pointee = .haveData
            return source.value
        }
        guard status != .error else { return markFailed() }
        guard output.frameLength > 0 else { return }
        do {
            try file.write(from: output)
            let frames = Int64(output.frameLength)
            lock.withLock {
                $0.totalFrames += frames
                $0.segmentFrames += frames
            }
        } catch {
            markFailed()
        }
    }

    private func markFailed() {
        lock.withLock { $0.failed = true }
    }

    private static func copy(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        guard let copy = AVAudioPCMBuffer(pcmFormat: buffer.format, frameCapacity: buffer.frameLength) else { return nil }
        copy.frameLength = buffer.frameLength
        let src = UnsafeMutableAudioBufferListPointer(UnsafeMutablePointer(mutating: buffer.audioBufferList))
        let dst = UnsafeMutableAudioBufferListPointer(copy.mutableAudioBufferList)
        for (s, d) in zip(src, dst) {
            guard let from = s.mData, let to = d.mData else { continue }
            memcpy(to, from, Int(min(s.mDataByteSize, d.mDataByteSize)))
        }
        return copy
    }

    private static func level(of buffer: AVAudioPCMBuffer) -> Float {
        guard let samples = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return 0 }
        let n = Int(buffer.frameLength)
        var sum: Float = 0
        for i in 0..<n { sum += samples[i] * samples[i] }
        let rms = (sum / Float(n)).squareRoot()
        guard rms > 0 else { return 0 }
        return min(1, max(0, (20 * log10(rms) + 60) / 60))
    }
}
