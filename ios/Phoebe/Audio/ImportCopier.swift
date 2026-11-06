import Foundation

// Streams the picked file into recordings/<id>/import.<ext> in 1 MB reads, hashing as it goes,
// so PendingUpload.sha256 needs no second pass over the file.
enum ImportCopier {
    struct Result: Equatable, Sendable {
        let url: URL
        let relativePath: String
        let sizeBytes: Int
        let sha256: String
    }

    static func copy(
        from source: URL,
        kind: ImportAudioKind,
        sizeBytes: Int64,
        meetingID: UUID,
        root: URL = RecordingPaths.root,
        chunkSize: Int = Checksum.chunkSize,
        progress: @Sendable (Double) -> Void = { _ in }
    ) async throws -> Result {
        let scoped = source.startAccessingSecurityScopedResource()
        defer { if scoped { source.stopAccessingSecurityScopedResource() } }

        let dir = try RecordingPaths.prepareDirectory(for: meetingID, root: root)
        let final = dir.appending(path: "import.\(kind.fileExtension)")
        let tmp = dir.appending(path: "import.\(kind.fileExtension).tmp")
        let fm = FileManager.default
        try? fm.removeItem(at: tmp)
        guard fm.createFile(atPath: tmp.path, contents: nil) else { throw CocoaError(.fileWriteUnknown) }

        do {
            let input = try FileHandle(forReadingFrom: source)
            defer { try? input.close() }
            let output = try FileHandle(forWritingTo: tmp)
            defer { try? output.close() }
            let total = max(1, sizeBytes)
            var stream = Checksum.Streaming()
            progress(0)
            while let data = try input.read(upToCount: chunkSize), !data.isEmpty {
                try Task.checkCancellation()
                try output.write(contentsOf: data)
                stream.update(data)
                progress(min(1, Double(stream.bytes) / Double(total)))
            }
            try output.synchronize()
            try? fm.removeItem(at: final)
            try fm.moveItem(at: tmp, to: final)
            progress(1)
            return Result(
                url: final,
                relativePath: RecordingPaths.relativePath(of: final, base: root.deletingLastPathComponent()),
                sizeBytes: stream.bytes,
                sha256: stream.finalizeHex()
            )
        } catch {
            try? fm.removeItem(at: tmp)
            throw error
        }
    }
}
