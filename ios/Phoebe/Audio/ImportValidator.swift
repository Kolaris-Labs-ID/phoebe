import AVFoundation
import Foundation

enum ImportAudioKind: String, Sendable, CaseIterable {
    case m4a, mp3, wav

    var contentType: String {
        switch self {
        case .m4a: "audio/mp4"
        case .mp3: "audio/mpeg"
        case .wav: "audio/wav"
        }
    }

    var fileExtension: String { rawValue }
}

enum ImportValidationError: Error, Equatable, Sendable {
    case tooBig(sizeBytes: Int64, limitBytes: Int64)
    case type
    case corrupt
    case empty
    case tooLong(durationMs: Int, limitMs: Int)
    case storage
}

struct ValidatedImport: Equatable, Sendable {
    let url: URL
    let fileName: String
    let kind: ImportAudioKind
    let sizeBytes: Int64
    let durationMs: Int
}

// ios.md §7: size → magic bytes (content, not extension) → AVURLAsset tracks/duration → free space.
struct ImportValidator: Sendable {
    static let defaultMaxUploadBytes: Int64 = 262_144_000

    var maxUploadBytes: Int64
    var maxDurationMs: Int
    var storage: any StorageProviding
    var headroomBytes: Int64 = RecordingConfig.standard.mergeHeadroomBytes

    init(maxUploadBytes: Int64, maxDurationMs: Int, storage: any StorageProviding = StorageMonitor()) {
        self.maxUploadBytes = maxUploadBytes
        self.maxDurationMs = maxDurationMs
        self.storage = storage
    }

    init(config: ServerConfig?, storage: any StorageProviding = StorageMonitor()) {
        self.init(
            maxUploadBytes: config.map { Int64($0.maxUploadBytes) } ?? Self.defaultMaxUploadBytes,
            maxDurationMs: config?.maxAudioDurationMs ?? ServerConfig.defaultMaxAudioDurationMs,
            storage: storage
        )
    }

    func validate(_ url: URL) async throws -> ValidatedImport {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }

        guard let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) else {
            throw ImportValidationError.corrupt
        }
        if size > maxUploadBytes { throw ImportValidationError.tooBig(sizeBytes: size, limitBytes: maxUploadBytes) }
        if size == 0 { throw ImportValidationError.empty }

        guard let kind = Self.sniff(try Self.header(of: url)) else { throw ImportValidationError.type }

        // Parse by sniffed content, not by the (possibly wrong) extension.
        let asset = AVURLAsset(url: url, options: [AVURLAssetOverrideMIMETypeKey: kind.contentType])
        guard let tracks = try? await asset.loadTracks(withMediaType: .audio), !tracks.isEmpty,
              let duration = try? await asset.load(.duration), duration.isNumeric
        else { throw ImportValidationError.corrupt }
        let durationMs = Int((duration.seconds * 1000).rounded())
        if durationMs < 1000 { throw ImportValidationError.empty }
        if durationMs > maxDurationMs { throw ImportValidationError.tooLong(durationMs: durationMs, limitMs: maxDurationMs) }

        if storage.freeBytes() < size + headroomBytes { throw ImportValidationError.storage }
        return ValidatedImport(url: url, fileName: url.lastPathComponent, kind: kind, sizeBytes: size, durationMs: durationMs)
    }

    static func header(of url: URL) throws -> Data {
        guard let handle = try? FileHandle(forReadingFrom: url) else { throw ImportValidationError.corrupt }
        defer { try? handle.close() }
        return (try? handle.read(upToCount: 12)) ?? Data()
    }

    static func sniff(_ header: Data) -> ImportAudioKind? {
        let b = [UInt8](header.prefix(12))
        if b.count >= 12, b[4...7] == [0x66, 0x74, 0x79, 0x70] { return .m4a }
        if b.count >= 12, b[0...3] == [0x52, 0x49, 0x46, 0x46], b[8...11] == [0x57, 0x41, 0x56, 0x45] { return .wav }
        if b.count >= 3, b[0...2] == [0x49, 0x44, 0x33] { return .mp3 }
        if b.count >= 2, b[0] == 0xFF, b[1] & 0xE0 == 0xE0 { return .mp3 }
        return nil
    }
}
