import Foundation

@MainActor
protocol ChunkTransport: AnyObject {
    func upload(_ request: URLRequest, fromFile file: URL, taskDescription: String) async throws -> (Data, URLResponse)
    func hasRunningTask(_ taskDescription: String) async -> Bool
    func cancel(_ taskDescription: String)
}

// One process-wide background session (a background identifier may have only one live URLSession).
// Completions that arrive with no awaiting caller (relaunch after suspension or termination) are
// forwarded as `.detached` so UploadManager can apply them and continue.
final class BackgroundUploadSession: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    enum Event: @unchecked Sendable {
        case progress(taskDescription: String, totalSent: Int64)
        case detached(taskDescription: String, data: Data, response: URLResponse?, error: APIError?)
    }

    static let identifier = "com.kolarislabs.phoebe.upload"
    static let shared = BackgroundUploadSession()

    private let lock = NSLock()
    private var waiters: [Int: CheckedContinuation<(Data, URLResponse), Error>] = [:]
    private var buffers: [Int: Data] = [:]
    private var handler: (@Sendable (Event) -> Void)?
    private var session: URLSession!

    private override init() {
        super.init()
        let config = URLSessionConfiguration.background(withIdentifier: Self.identifier)
        config.sessionSendsLaunchEvents = true
        config.isDiscretionary = false
        session = URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }

    func setHandler(_ handler: @escaping @Sendable (Event) -> Void) {
        lock.withLock { self.handler = handler }
    }

    func upload(_ request: URLRequest, fromFile file: URL, taskDescription: String) async throws -> (Data, URLResponse) {
        try await withCheckedThrowingContinuation { continuation in
            let task = session.uploadTask(with: request, fromFile: file)
            task.taskDescription = taskDescription
            lock.withLock { waiters[task.taskIdentifier] = continuation }
            task.resume()
        }
    }

    func hasRunningTask(_ taskDescription: String) async -> Bool {
        await session.allTasks.contains { $0.taskDescription == taskDescription && $0.state == .running }
    }

    func cancel(_ taskDescription: String) {
        session.getAllTasks { tasks in
            tasks.filter { $0.taskDescription == taskDescription }.forEach { $0.cancel() }
        }
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        lock.withLock { buffers[dataTask.taskIdentifier, default: Data()].append(data) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64, totalBytesSent: Int64, totalBytesExpectedToSend: Int64) {
        guard let description = task.taskDescription else { return }
        lock.withLock { handler }?(.progress(taskDescription: description, totalSent: totalBytesSent))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        let id = task.taskIdentifier
        let (waiter, data, handler) = lock.withLock { (waiters.removeValue(forKey: id), buffers.removeValue(forKey: id) ?? Data(), self.handler) }
        if let waiter {
            if let error {
                waiter.resume(throwing: error)
            } else if let response = task.response {
                waiter.resume(returning: (data, response))
            } else {
                waiter.resume(throwing: URLError(.badServerResponse))
            }
        } else if let description = task.taskDescription {
            handler?(.detached(taskDescription: description, data: data, response: task.response, error: error.map(APIError.from)))
        }
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        Task { @MainActor in BackgroundSessionEvents.shared.complete(identifier: Self.identifier) }
    }
}

@MainActor
final class BackgroundChunkTransport: ChunkTransport {
    func upload(_ request: URLRequest, fromFile file: URL, taskDescription: String) async throws -> (Data, URLResponse) {
        try await BackgroundUploadSession.shared.upload(request, fromFile: file, taskDescription: taskDescription)
    }

    func hasRunningTask(_ taskDescription: String) async -> Bool {
        await BackgroundUploadSession.shared.hasRunningTask(taskDescription)
    }

    func cancel(_ taskDescription: String) {
        BackgroundUploadSession.shared.cancel(taskDescription)
    }
}
