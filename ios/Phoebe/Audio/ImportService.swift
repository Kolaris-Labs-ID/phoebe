import Foundation

// Implements the Library seam (Features/Library/ImportModel.swift) with ImportValidator + ImportCopier.
@MainActor
final class AudioImportService: ImportServicing {
    private let storage: any StorageProviding
    private let root: URL

    init(storage: any StorageProviding = StorageMonitor(), root: URL = RecordingPaths.root) {
        self.storage = storage
        self.root = root
    }

    func validate(_ url: URL, limits: ImportLimits) async -> Result<ImportCandidate, ImportFailure> {
        let validator = ImportValidator(maxUploadBytes: Int64(limits.maxUploadBytes), maxDurationMs: limits.maxDurationMs, storage: storage)
        do {
            let file = try await validator.validate(url)
            return .success(ImportCandidate(
                sourceURL: url, fileName: file.fileName, fileExtension: file.kind.fileExtension,
                contentType: file.kind.contentType, sizeBytes: Int(file.sizeBytes), durationMs: file.durationMs
            ))
        } catch let error as ImportValidationError {
            return .failure(error.failure)
        } catch {
            return .failure(.corrupt)
        }
    }

    func copy(_ candidate: ImportCandidate, meetingID: UUID, progress: @escaping @MainActor (Double) -> Void) async -> Result<ImportedFile, ImportFailure> {
        let kind = ImportAudioKind(rawValue: candidate.fileExtension) ?? .m4a
        do {
            let result = try await ImportCopier.copy(
                from: candidate.sourceURL, kind: kind, sizeBytes: Int64(candidate.sizeBytes), meetingID: meetingID, root: root,
                progress: { value in Task { @MainActor in progress(value) } }
            )
            return .success(ImportedFile(relativePath: result.relativePath, sizeBytes: result.sizeBytes, sha256: result.sha256))
        } catch {
            try? FileManager.default.removeItem(at: RecordingPaths.directory(for: meetingID, root: root))
            return .failure(SegmentMerger.isOutOfSpace(error) ? .storage : .corrupt)
        }
    }
}

extension ImportValidationError {
    var failure: ImportFailure {
        switch self {
        case .tooBig(let size, _): .tooBig(sizeBytes: Int(size))
        case .type: .type
        case .corrupt: .corrupt
        case .empty: .empty
        case .tooLong(let duration, _): .tooLong(durationMs: duration)
        case .storage: .storage
        }
    }
}

extension ImportFailure {
    func localizedMessage(maxUploadBytes: Int, maxDurationMs: Int) -> LocalizedStringResource {
        switch self {
        case .type: L("import.error.type", table: .audio)
        case .corrupt: L("import.error.corrupt", table: .audio)
        case .empty: L("import.error.empty", table: .audio)
        case .storage: L("import.error.storage", table: .audio)
        case .tooBig(let size):
            L("import.error.tooBig", table: .audio,
              ByteCountFormatter.string(fromByteCount: Int64(size), countStyle: .file), Int64(maxUploadBytes / 1_048_576))
        case .tooLong(let duration):
            L("import.error.tooLong", table: .audio,
              Formatting.spokenDuration(ms: duration, includeSeconds: false), Int64(maxDurationMs / 60_000))
        }
    }
}
