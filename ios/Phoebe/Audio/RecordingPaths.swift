import AVFoundation
import Foundation

// PendingUpload.filePath is stored relative to `base` (Application Support): the app container's
// absolute path can change between launches/updates.
enum RecordingPaths {
    static var base: URL { .applicationSupportDirectory }
    static var root: URL { base.appending(path: "recordings", directoryHint: .isDirectory) }
    static let mergedFileName = "audio.m4a"
    static let mergedTempFileName = "audio.m4a.tmp"

    static func directory(for meetingID: UUID, root: URL = root) -> URL {
        root.appending(path: meetingID.lower, directoryHint: .isDirectory)
    }

    @discardableResult
    static func prepareDirectory(for meetingID: UUID, root: URL = root) throws -> URL {
        let dir = directory(for: meetingID, root: root)
        let fm = FileManager.default
        // Lock-screen recording needs writes while locked, so not `.complete`.
        let attributes: [FileAttributeKey: Any] = [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]
        if !fm.fileExists(atPath: root.path) {
            try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: attributes)
            var rootURL = root
            var values = URLResourceValues()
            values.isExcludedFromBackup = true
            try? rootURL.setResourceValues(values)
        }
        if !fm.fileExists(atPath: dir.path) {
            try fm.createDirectory(at: dir, withIntermediateDirectories: true, attributes: attributes)
        }
        return dir
    }

    static func relativePath(of url: URL, base: URL = base) -> String {
        let basePath = base.standardizedFileURL.path
        let path = url.standardizedFileURL.path
        guard path.hasPrefix(basePath + "/") else { return path }
        return String(path.dropFirst(basePath.count + 1))
    }

    static func resolve(_ path: String, base: URL = base) -> URL {
        path.hasPrefix("/") ? URL(filePath: path) : base.appending(path: path)
    }

    static func fileSize(_ url: URL) -> Int64 {
        ((try? FileManager.default.attributesOfItem(atPath: url.path)[.size]) as? NSNumber)?.int64Value ?? 0
    }

    static func segmentIndex(fileName: String) -> Int? {
        guard fileName.hasPrefix("seg-"), fileName.hasSuffix(".m4a") else { return nil }
        let digits = fileName.dropFirst(4).dropLast(4)
        guard digits.count == 4, digits.allSatisfy(\.isNumber) else { return nil }
        return Int(digits)
    }
}

extension PendingUpload {
    var fileURL: URL { RecordingPaths.resolve(filePath) }
}

enum AudioFiles {
    static func playableDurationMs(_ url: URL) async -> Int? {
        let asset = AVURLAsset(url: url)
        guard let (playable, duration) = try? await asset.load(.isPlayable, .duration),
              playable, duration.isNumeric,
              let tracks = try? await asset.loadTracks(withMediaType: .audio), !tracks.isEmpty
        else { return nil }
        let ms = Int((duration.seconds * 1000).rounded())
        return ms > 0 ? ms : nil
    }
}
