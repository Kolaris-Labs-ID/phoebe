import Foundation
import Testing
@testable import Phoebe

struct ExportCacheTests {
    let directory = FileManager.default.temporaryDirectory.appending(path: "export-cache-tests/\(UUID().uuidString)", directoryHint: .isDirectory)

    func export(revisionID: UUID, isDraft: Bool, format: ExportFormat = .pdf) -> ExportDTO {
        ExportDTO(
            id: UUID(), documentRevisionId: revisionID, kind: .mom, format: format,
            filename: "weekly-sync-20261106-mom\(isDraft ? "-draft" : "").pdf", contentType: "application/pdf",
            sizeBytes: 3, isDraft: isDraft, createdAt: .now
        )
    }

    func download(_ text: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appending(path: "dl-\(UUID().uuidString)")
        try Data(text.utf8).write(to: url)
        return url
    }

    @Test func keyCarriesDraftOrReviewedSuffix() {
        let rev = UUID()
        #expect(ExportCache.key(revisionID: rev, isDraft: true, format: .pdf) == "\(rev.lower)-draft.pdf")
        #expect(ExportCache.key(revisionID: rev, isDraft: false, format: .pdf) == "\(rev.lower)-reviewed.pdf")
    }

    @Test func offlineHitOnlyWhenSuffixMatchesCurrentReviewStatus() throws {
        let cache = ExportCache(directory: directory)
        let rev = UUID()
        _ = try cache.store(downloaded: try download("pdf"), export: export(revisionID: rev, isDraft: true), text: nil)

        let draft = try #require(cache.lookup(revisionID: rev, reviewStatus: .draft, format: .pdf))
        #expect(draft.fileURL.lastPathComponent == "\(rev.lower)-draft.pdf")
        #expect(draft.filename == "weekly-sync-20261106-mom-draft.pdf")
        #expect(cache.lookup(revisionID: rev, reviewStatus: .reviewed, format: .pdf) == nil)
        #expect(cache.lookup(revisionID: rev, reviewStatus: .draft, format: .docx) == nil)

        _ = try cache.store(downloaded: try download("pdf"), export: export(revisionID: rev, isDraft: false), text: nil)
        #expect(cache.lookup(revisionID: rev, reviewStatus: .reviewed, format: .pdf)?.isDraft == false)
    }

    @Test func meetingDeletionRemovesOnlyThatMeetingsRevisions() throws {
        let cache = ExportCache(directory: directory)
        let gone = UUID(), kept = UUID()
        _ = try cache.store(downloaded: try download("a"), export: export(revisionID: gone, isDraft: true), text: nil)
        _ = try cache.store(downloaded: try download("b"), export: export(revisionID: gone, isDraft: false), text: nil)
        _ = try cache.store(downloaded: try download("c"), export: export(revisionID: kept, isDraft: true), text: nil)

        SyncFiles.removeExports(revisionIDs: [gone], directory: directory)

        #expect(cache.lookup(revisionID: gone, reviewStatus: .draft, format: .pdf) == nil)
        #expect(cache.lookup(revisionID: gone, reviewStatus: .reviewed, format: .pdf) == nil)
        #expect(cache.lookup(revisionID: kept, reviewStatus: .draft, format: .pdf) != nil)
        let names = try FileManager.default.contentsOfDirectory(atPath: directory.path)
        #expect(names.allSatisfy { $0.hasPrefix(kept.lower) })
    }
}
