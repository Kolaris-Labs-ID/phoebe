import Foundation

enum OutboxResult: Equatable {
    case done
    // Item kept but parked (failedCode / conflict); later items continue.
    case blocked
    // Ambiguous or transient failure: stop this queue, retry with backoff.
    case retry
    // Stop this queue without scheduling a retry (needs user action).
    case halt
    // Offline or signed out: stop everything.
    case stop
    // 404 on a meeting-scoped call: confirm the meeting.
    case missing

    static func classify(_ error: APIError) -> OutboxResult {
        switch error {
        case .unauthenticated, .offline: .stop
        case .notFound: .missing
        case .uploadBusy, .jobInProgress: .retry
        default:
            if error.isAmbiguous { .retry }
            else if case .decoding = error { .retry }
            else { .blocked }
        }
    }
}

enum JSONPayload {
    static func object(_ data: Data) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
    }

    static func data(_ object: [String: Any]) -> Data {
        (try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes])) ?? Data("{}".utf8)
    }
}
