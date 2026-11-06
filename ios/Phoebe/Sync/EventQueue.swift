import Foundation
import SwiftData

// PRD §10 events (ios.md §14): persisted first, flushed in batches of ≤ 100; the server dedups by event_id.
@MainActor
final class EventQueue: EventQueueAPI {
    static let batchSize = 100

    private let env: ServiceEnvironment
    private var isFlushing = false

    init(env: ServiceEnvironment) {
        self.env = env
    }

    func log(_ event: AnalyticsEvent, meetingID: UUID?) {
        env.context.insert(PendingEvent(name: event, meetingID: meetingID))
        SyncStore(context: env.context).save()
    }

    func flush() async {
        guard env.app.isOnline, !isFlushing else { return }
        isFlushing = true
        defer { isFlushing = false }
        let store = SyncStore(context: env.context)
        while true {
            var descriptor = FetchDescriptor<PendingEvent>(sortBy: [SortDescriptor(\.occurredAt)])
            descriptor.fetchLimit = Self.batchSize
            guard let rows = try? env.context.fetch(descriptor), !rows.isEmpty else { return }
            let valid = rows.compactMap { row in row.dto.map { (row, $0) } }
            rows.filter { $0.dto == nil }.forEach(env.context.delete)
            guard !valid.isEmpty else {
                store.save()
                continue
            }
            do {
                _ = try await env.api.send(Endpoints.events(valid.map(\.1)))
                valid.forEach { env.context.delete($0.0) }
                store.save()
            } catch APIError.validationFailed {
                // Bug path: a batch the server rejects can never succeed; drop it.
                valid.forEach { env.context.delete($0.0) }
                store.save()
            } catch {
                store.save()
                return
            }
        }
    }
}
