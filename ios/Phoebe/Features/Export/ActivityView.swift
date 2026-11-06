import QuickLook
import SwiftUI
import UIKit
import UniformTypeIdentifiers

// UIKit presenters: the share sheet needs a file URL with the right name/UTI, and presenting it from
// the top controller avoids nesting it inside the SwiftUI sheet.
@MainActor
enum TopViewController {
    static func present(_ controller: UIViewController) {
        let root = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .first { $0.isKeyWindow }?
            .rootViewController
        var top = root
        while let presented = top?.presentedViewController { top = presented }
        top?.present(controller, animated: true)
    }
}

final class QuickLookSource: NSObject, QLPreviewControllerDataSource {
    let url: URL

    init(url: URL) { self.url = url }

    @MainActor func controller() -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = self
        return controller
    }

    func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }

    func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem {
        url as NSURL
    }
}

struct ExportFileDocument: FileDocument {
    static let readableContentTypes: [UTType] = ExportFormat.allCases.map(\.contentType)

    let data: Data
    let contentType: UTType

    init(data: Data, contentType: UTType) {
        self.data = data
        self.contentType = contentType
    }

    init(configuration: ReadConfiguration) throws {
        data = configuration.file.regularFileContents ?? Data()
        contentType = configuration.contentType
    }

    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: data)
    }
}
