import Foundation
import QuickLookThumbnailing
import SwiftUI
import UIKit

@MainActor @Observable
final class ExportModel {
    enum State: Equatable {
        case idle, noDocument, syncing, offlinePending, offline, conflict, generating, failed
        case ready(ExportArtifact)
    }

    struct Inputs: Equatable {
        var kind: DocumentKind
        var format: ExportFormat
        var revisionID: UUID?
        var reviewStatus: ReviewStatus
        var pendingWork: Bool
        var conflict: Bool
        var online: Bool
        var retry: Int
    }

    let meetingID: UUID
    var kind: DocumentKind
    var format: ExportFormat = .pdf
    var retryToken = 0
    var showConflict = false
    var showFileExporter = false
    private(set) var state: State = .idle
    private(set) var thumbnail: UIImage?
    private(set) var fileDocument: ExportFileDocument?

    @ObservationIgnored private let app: AppModel
    @ObservationIgnored private let cache: ExportCache
    @ObservationIgnored private var quickLook: QuickLookSource?

    init(meetingID: UUID, kind: DocumentKind, app: AppModel, cache: ExportCache = .standard) {
        self.meetingID = meetingID
        self.kind = kind
        self.app = app
        self.cache = cache
    }

    var artifact: ExportArtifact? {
        if case .ready(let a) = state { a } else { nil }
    }

    // Online, every document/format change renders a fresh export (title and Draft label may have
    // changed); the cache is only refreshed. Offline, a cache hit must match the current review status.
    func refresh(_ inputs: Inputs) async {
        thumbnail = nil
        fileDocument = nil
        if inputs.conflict {
            state = .conflict
            showConflict = true
            return
        }
        if inputs.pendingWork {
            guard inputs.online else {
                state = .offlinePending
                return
            }
            state = .syncing
            let ok = await app.sync.flushEdits(meetingID: meetingID, kinds: [inputs.kind])
            if !ok, !Task.isCancelled { state = .failed }
            return
        }
        guard let revisionID = inputs.revisionID else {
            state = .noDocument
            return
        }
        guard inputs.online else {
            if let hit = cache.lookup(revisionID: revisionID, reviewStatus: inputs.reviewStatus, format: inputs.format) {
                await setReady(hit)
            } else {
                state = .offline
            }
            return
        }
        state = .generating
        do {
            let response = try await app.api.send(Endpoints.createExport(documentRevisionID: revisionID, format: inputs.format))
            let artifact = try await download(response, text: response.text, retried: false)
            guard !Task.isCancelled else { return }
            app.events.log(.exportCompleted, meetingID: meetingID)
            await setReady(artifact)
        } catch {
            guard !Task.isCancelled else { return }
            state = .failed
        }
    }

    // A signed URL that expired before the download gets one fresh URL via GET /v1/exports/{id}.
    private func download(_ response: ExportResponse, text: String?, retried: Bool) async throws -> ExportArtifact {
        let (tmp, urlResponse) = try await URLSession.shared.download(from: response.downloadUrl)
        let status = (urlResponse as? HTTPURLResponse)?.statusCode ?? 0
        if status == 410 || status == 403, !retried {
            try? FileManager.default.removeItem(at: tmp)
            let fresh = try await app.api.send(Endpoints.export(response.export.id))
            return try await download(fresh, text: text, retried: true)
        }
        guard (200..<300).contains(status) else {
            try? FileManager.default.removeItem(at: tmp)
            throw APIError.server(status: status, code: nil)
        }
        return try cache.store(downloaded: tmp, export: response.export, text: text)
    }

    private func setReady(_ artifact: ExportArtifact) async {
        state = .ready(artifact)
        if let url = try? ExportCache.namedCopy(of: artifact), let data = try? Data(contentsOf: url) {
            fileDocument = ExportFileDocument(data: data, contentType: artifact.format.contentType)
        }
        guard !artifact.format.isText else { return }
        let request = QLThumbnailGenerator.Request(fileAt: artifact.fileURL, size: CGSize(width: 360, height: 480),
                                                   scale: UITraitCollection.current.displayScale, representationTypes: .thumbnail)
        thumbnail = try? await QLThumbnailGenerator.shared.generateBestRepresentation(for: request).uiImage
    }

    // MARK: Actions

    func share() {
        guard let artifact else { return }
        let items: [Any]
        if artifact.format == .whatsapp, let text = artifact.text {
            items = [text]
        } else {
            guard let url = try? ExportCache.namedCopy(of: artifact) else { return }
            items = [url]
        }
        // Completion and cancel do nothing: no toast, no "sent" claim, no re-export.
        TopViewController.present(UIActivityViewController(activityItems: items, applicationActivities: nil))
        app.events.log(.shareSheetOpened, meetingID: meetingID)
    }

    func copyText() {
        guard let text = artifact?.text else { return }
        UIPasteboard.general.string = text
        app.toasts.show(L("export.copied", table: .export))
    }

    func openPreview() {
        guard let artifact, let url = try? ExportCache.namedCopy(of: artifact) else { return }
        let source = QuickLookSource(url: url)
        quickLook = source
        TopViewController.present(source.controller())
    }

    func savedToFiles(_ result: Result<URL, Error>) {
        switch result {
        case .success: app.toasts.show(L("export.savedFiles", table: .export))
        case .failure(let error as CocoaError) where error.code == .userCancelled: break
        case .failure: app.toasts.show(L("export.saveFailed", table: .export))
        }
    }
}
