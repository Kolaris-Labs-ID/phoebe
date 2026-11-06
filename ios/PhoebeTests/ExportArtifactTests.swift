import Foundation
import Testing
@testable import Phoebe

struct ExportArtifactTests {
    let revisionID = UUID()
    let cache = ExportCache(directory: FileManager.default.temporaryDirectory.appending(path: "export-artifact-tests/\(UUID().uuidString)", directoryHint: .isDirectory))

    func export(isDraft: Bool, format: ExportFormat) -> ExportDTO {
        let ext = format == .whatsapp ? "txt" : format.rawValue
        return ExportDTO(id: UUID(), documentRevisionId: revisionID, kind: .mom, format: format,
                         filename: "weekly-sync-20261106-mom\(isDraft ? "-draft" : "").\(ext)",
                         contentType: "text/plain; charset=utf-8", sizeBytes: 4, isDraft: isDraft, createdAt: Date(timeIntervalSince1970: 0))
    }

    func download(_ text: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appending(path: "dl-\(UUID().uuidString)")
        try Data(text.utf8).write(to: url)
        return url
    }

    @Test func reviewedTextExportKeepsTextAndIsSeparateFromDraft() throws {
        _ = try cache.store(downloaded: try download("*Weekly*"), export: export(isDraft: false, format: .whatsapp), text: "*Weekly*")
        let hit = try #require(cache.lookup(revisionID: revisionID, reviewStatus: .reviewed, format: .whatsapp))
        #expect(hit.text == "*Weekly*")
        #expect(!hit.isDraft)
        #expect(cache.lookup(revisionID: revisionID, reviewStatus: .draft, format: .whatsapp) == nil)
    }

    @Test func refreshReplacesFileAndNamedCopyUsesServerFilename() throws {
        _ = try cache.store(downloaded: try download("# v1"), export: export(isDraft: true, format: .md), text: nil)
        let second = try cache.store(downloaded: try download("# v2"), export: export(isDraft: true, format: .md), text: nil)
        #expect(try String(contentsOf: second.fileURL, encoding: .utf8) == "# v2")
        #expect(cache.lookup(revisionID: revisionID, reviewStatus: .draft, format: .md)?.text == "# v2")
        let named = try ExportCache.namedCopy(of: second)
        #expect(named.lastPathComponent == "weekly-sync-20261106-mom-draft.md")
        #expect(try String(contentsOf: named, encoding: .utf8) == "# v2")
    }

    @Test func formatContentTypes() {
        #expect(ExportFormat.pdf.contentType == .pdf)
        #expect(ExportFormat.whatsapp.contentType == .plainText)
        #expect(ExportFormat.md.isText && ExportFormat.whatsapp.isText && !ExportFormat.docx.isText)
    }
}
