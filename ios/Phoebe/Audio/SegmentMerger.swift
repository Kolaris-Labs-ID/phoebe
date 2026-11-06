import AVFoundation

enum MergeError: Error {
    case noSegments, unreadableSegment(URL), exportUnavailable, exportFailed(Error?)
}

// Concatenates closed segments without re-encoding into `audio.m4a.tmp`, fsyncs, then renames it
// to `audio.m4a`, so a crash mid-merge never leaves a truncated file under the final name.
struct SegmentMerger: Sendable {
    func merge(
        segments: [URL],
        into directory: URL,
        beforeRename: (@Sendable (_ tmp: URL, _ final: URL) -> Void)? = nil
    ) async throws -> URL {
        guard !segments.isEmpty else { throw MergeError.noSegments }
        let tmp = directory.appending(path: RecordingPaths.mergedTempFileName)
        let final = directory.appending(path: RecordingPaths.mergedFileName)
        try? FileManager.default.removeItem(at: tmp)

        let composition = AVMutableComposition()
        guard let track = composition.addMutableTrack(withMediaType: .audio, preferredTrackID: kCMPersistentTrackID_Invalid) else {
            throw MergeError.exportUnavailable
        }
        var cursor = CMTime.zero
        for url in segments {
            let asset = AVURLAsset(url: url)
            guard let source = try await asset.loadTracks(withMediaType: .audio).first else {
                throw MergeError.unreadableSegment(url)
            }
            let range = try await source.load(.timeRange)
            try track.insertTimeRange(range, of: source, at: cursor)
            cursor = cursor + range.duration
        }

        // Passthrough refuses `.m4a` for a multi-segment composition but accepts `.mp4` (same ISO BMFF
        // container, AAC untouched, still `audio/mp4`); re-encoding with AppleM4A is the last resort.
        var choice: (preset: String, type: AVFileType)?
        for option in [(AVAssetExportPresetPassthrough, AVFileType.m4a), (AVAssetExportPresetPassthrough, .mp4), (AVAssetExportPresetAppleM4A, .m4a)] {
            if await AVAssetExportSession.compatibility(ofExportPreset: option.0, with: composition, outputFileType: option.1) {
                choice = option
                break
            }
        }
        guard let choice, let session = AVAssetExportSession(asset: composition, presetName: choice.preset) else {
            throw MergeError.exportUnavailable
        }
        do {
            if #available(iOS 18.0, *) {
                try await session.export(to: tmp, as: choice.type)
            } else {
                session.outputURL = tmp
                session.outputFileType = choice.type
                await session.export()
                guard session.status == .completed else { throw MergeError.exportFailed(session.error) }
            }
        } catch {
            try? FileManager.default.removeItem(at: tmp)
            throw error
        }

        let handle = try FileHandle(forUpdating: tmp)
        try handle.synchronize()
        try handle.close()
        beforeRename?(tmp, final)
        guard rename(tmp.path, final.path) == 0 else {
            throw CocoaError(.fileWriteUnknown, userInfo: [NSUnderlyingErrorKey: POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)])
        }
        return final
    }

    static func isOutOfSpace(_ error: Error) -> Bool {
        var current: Error? = error
        while let e = current as NSError? {
            if e.domain == NSCocoaErrorDomain && e.code == NSFileWriteOutOfSpaceError { return true }
            if e.domain == NSPOSIXErrorDomain && e.code == Int(ENOSPC) { return true }
            if e.domain == AVFoundationErrorDomain && e.code == AVError.diskFull.rawValue { return true }
            current = e.userInfo[NSUnderlyingErrorKey] as? Error
        }
        return false
    }
}
