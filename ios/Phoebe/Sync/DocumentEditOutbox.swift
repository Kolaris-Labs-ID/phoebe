import Foundation
import SwiftData

// ios.md §10 "Document edits": the in-flight snapshot is frozen and never mutated, so a retry
// always resends the identical body with the same mutation id.
@MainActor
struct DocumentEditOutbox {
    let env: ServiceEnvironment
    private var store: SyncStore { SyncStore(context: env.context) }

    static let order: [DocumentKind] = [.manualNotes, .aiNotes, .mom]

    func saveDraft(meetingID: UUID, kind: DocumentKind, wireContent: Data, loadedRevision: Int) {
        if let row = store.edit(meetingID, kind) {
            row.content = wireContent
            row.failedCode = nil
            row.updatedAt = .now
        } else {
            env.context.insert(PendingDocumentEdit(meetingID: meetingID, kind: kind, content: wireContent, baseRevision: loadedRevision))
        }
        store.save()
    }

    func push(_ meetingID: UUID, kinds: [DocumentKind] = order) async -> OutboxResult {
        var result = OutboxResult.done
        for kind in Self.order where kinds.contains(kind) {
            guard let row = store.edit(meetingID, kind) else { continue }
            switch await push(row) {
            case .stop: return .stop
            case .missing: return .missing
            case .retry: result = .retry
            default: break
            }
        }
        return result
    }

    private func push(_ row: PendingDocumentEdit) async -> OutboxResult {
        guard row.conflictCurrentRevision == nil, row.failedCode == nil else { return .blocked }
        var replacedReusedKey = false
        while true {
            if row.inFlightMutationID == nil {
                guard let content = row.content else {
                    env.context.delete(row)
                    store.save()
                    return .done
                }
                row.inFlightMutationID = UUID()
                row.inFlightContent = content
                row.inFlightExpected = row.expectedRevision
                row.inFlightBase = row.baseRevision
                store.save()
            }
            guard let mutationID = row.inFlightMutationID, let content = row.inFlightContent,
                  let expected = row.inFlightExpected, let base = row.inFlightBase else { return .blocked }
            let meetingID = row.meetingID, kind = row.kind
            let endpoint = Endpoints.putDocument(meetingID: meetingID, kind: kind, expectedRevision: expected, baseRevision: base, mutationID: mutationID, content: content)
            do {
                let response = try await env.api.send(endpoint)
                guard !row.isDeleted, row.modelContext != nil else { return .done }
                MeetingMirror(store: store).upsert(response, meetingID: meetingID)
                env.app.events.log(.documentSaved, meetingID: meetingID)
                let revision = response.revision?.revisionNumber ?? response.document.currentRevisionNumber
                clearInFlight(row)
                if row.content == content {
                    env.context.delete(row)
                    store.save()
                    return .done
                }
                row.baseRevision = revision
                row.expectedRevision = revision
                store.save()
            } catch let error as APIError {
                guard !row.isDeleted, row.modelContext != nil else { return .done }
                switch error {
                case .revisionConflict(let current, _):
                    clearInFlight(row)
                    row.conflictCurrentRevision = current
                    store.save()
                    await SyncRemote.refreshDocument(meetingID, kind: kind, env: env)
                    return .blocked
                case .idempotencyKeyReused where !replacedReusedKey:
                    replacedReusedKey = true
                    clearInFlight(row)
                    store.save()
                default:
                    let result = OutboxResult.classify(error)
                    if result == .blocked {
                        clearInFlight(row)
                        row.failedCode = error.code
                        store.save()
                    }
                    return result
                }
            } catch {
                return .retry
            }
        }
    }

    private func clearInFlight(_ row: PendingDocumentEdit) {
        row.inFlightMutationID = nil
        row.inFlightContent = nil
        row.inFlightExpected = nil
        row.inFlightBase = nil
    }

    func resolveConflict(meetingID: UUID, kind: DocumentKind, _ resolution: ConflictResolution) async {
        guard let row = store.edit(meetingID, kind) else { return }
        switch resolution {
        case .keepMine:
            guard let current = row.conflictCurrentRevision else { return }
            row.expectedRevision = current
            row.conflictCurrentRevision = nil
            store.save()
        case .useLatest:
            env.context.delete(row)
            store.save()
            await SyncRemote.refreshDocument(meetingID, kind: kind, env: env)
        }
    }

    func isClean(_ meetingID: UUID, kinds: [DocumentKind]) -> Bool {
        kinds.allSatisfy { kind in
            guard let row = store.edit(meetingID, kind) else { return true }
            return !row.hasPendingWork && row.conflictCurrentRevision == nil && row.failedCode == nil
        }
    }
}
