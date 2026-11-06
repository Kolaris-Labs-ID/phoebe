import Foundation
import UniformTypeIdentifiers

struct ExportArtifact: Equatable, Sendable {
    let exportID: UUID
    let revisionID: UUID
    let format: ExportFormat
    let filename: String
    let isDraft: Bool
    let fileURL: URL
    let text: String?
    let createdAt: Date
}

extension ExportFormat {
    var contentType: UTType {
        switch self {
        case .pdf: .pdf
        case .docx: UTType("org.openxmlformats.wordprocessingml.document") ?? .data
        case .md: UTType(filenameExtension: "md") ?? .plainText
        case .whatsapp: .plainText
        }
    }

    var isText: Bool { self == .md || self == .whatsapp }
}

// Caches/exports/<document_revision_id>-<draft|reviewed>.<format> plus a JSON sidecar with the
// server filename. Content is immutable per revision; the Draft label is not, hence the suffix.
struct ExportCache: Sendable {
    let directory: URL

    static var standard: ExportCache {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        return ExportCache(directory: caches.appending(path: "exports", directoryHint: .isDirectory))
    }

    private struct Sidecar: Codable {
        let exportID: UUID
        let filename: String
        let isDraft: Bool
        let text: String?
        let createdAt: Date
    }

    static func key(revisionID: UUID, isDraft: Bool, format: ExportFormat) -> String {
        "\(revisionID.lower)-\(isDraft ? "draft" : "reviewed").\(format.rawValue)"
    }

    func fileURL(revisionID: UUID, isDraft: Bool, format: ExportFormat) -> URL {
        directory.appending(path: Self.key(revisionID: revisionID, isDraft: isDraft, format: format))
    }

    private func sidecarURL(_ file: URL) -> URL { file.appendingPathExtension("json") }

    func store(downloaded source: URL, export: ExportDTO, text: String?) throws -> ExportArtifact {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let dest = fileURL(revisionID: export.documentRevisionId, isDraft: export.isDraft, format: export.format)
        if FileManager.default.fileExists(atPath: dest.path) {
            _ = try FileManager.default.replaceItemAt(dest, withItemAt: source)
        } else {
            try FileManager.default.moveItem(at: source, to: dest)
        }
        let sidecar = Sidecar(exportID: export.id, filename: export.filename, isDraft: export.isDraft, text: text, createdAt: export.createdAt)
        try JSONEncoder().encode(sidecar).write(to: sidecarURL(dest), options: .atomic)
        return ExportArtifact(exportID: export.id, revisionID: export.documentRevisionId, format: export.format, filename: export.filename,
                              isDraft: export.isDraft, fileURL: dest, text: text, createdAt: export.createdAt)
    }

    // Offline only: a hit requires the suffix to match the document's current review status.
    func lookup(revisionID: UUID, reviewStatus: ReviewStatus, format: ExportFormat) -> ExportArtifact? {
        let isDraft = reviewStatus != .reviewed
        let file = fileURL(revisionID: revisionID, isDraft: isDraft, format: format)
        guard FileManager.default.fileExists(atPath: file.path),
              let data = try? Data(contentsOf: sidecarURL(file)),
              let sidecar = try? JSONDecoder().decode(Sidecar.self, from: data) else { return nil }
        let text = sidecar.text ?? (format.isText ? (try? String(contentsOf: file, encoding: .utf8)) : nil)
        return ExportArtifact(exportID: sidecar.exportID, revisionID: revisionID, format: format, filename: sidecar.filename,
                              isDraft: isDraft, fileURL: file, text: text, createdAt: sidecar.createdAt)
    }

    // Share sheet and Files use the server filename as-is.
    static func namedCopy(of artifact: ExportArtifact) throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appending(path: "exports/\(artifact.exportID.lower)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let dest = dir.appending(path: artifact.filename)
        if !FileManager.default.fileExists(atPath: dest.path) {
            try FileManager.default.copyItem(at: artifact.fileURL, to: dest)
        }
        return dest
    }
}
