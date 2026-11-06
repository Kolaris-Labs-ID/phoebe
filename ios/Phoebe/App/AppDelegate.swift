import UIKit

@MainActor
final class BackgroundSessionEvents {
    static let shared = BackgroundSessionEvents()
    private var handlers: [String: () -> Void] = [:]

    func store(identifier: String, completion: @escaping () -> Void) {
        handlers[identifier] = completion
    }

    // UploadManager calls this from urlSessionDidFinishEvents(forBackgroundURLSession:), on main.
    func complete(identifier: String) {
        handlers.removeValue(forKey: identifier)?()
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        handleEventsForBackgroundURLSession identifier: String,
        completionHandler: @escaping () -> Void
    ) {
        BackgroundSessionEvents.shared.store(identifier: identifier, completion: completionHandler)
    }
}
