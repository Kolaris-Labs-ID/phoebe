import Foundation

// Sends a persisted PendingIntent with its stored key and exact body (api §1.4, ios.md §8 step 7).
@MainActor
enum IntentSender {
    enum Outcome: Equatable {
        case done
        case retryLater(APIError?)
        case waiting(String)
        case rejected(APIError)
        case missing
        case skipped
    }

    private static var inFlight: Set<UUID> = []

    static func send(_ intent: PendingIntent, env: ServiceEnvironment) async -> Outcome {
        let key = intent.idempotencyKey
        guard !inFlight.contains(key) else { return .skipped }
        inFlight.insert(key)
        defer { inFlight.remove(key) }

        let store = SyncStore(context: env.context)
        let meetingID = intent.meetingID, type = intent.type, body = intent.body
        intent.keySent = true
        store.save()

        do {
            let job: JobDTO = switch type {
            case .transcribe: try await env.api.send(Endpoints.startTranscription(meetingID: meetingID, idempotencyKey: key, body: body)).job
            case .generateNotes: try await env.api.send(Endpoints.generateNotes(meetingID: meetingID, idempotencyKey: key, body: body)).job
            case .generateMom: try await env.api.send(Endpoints.generateMoM(meetingID: meetingID, idempotencyKey: key, body: body)).job
            }
            MeetingMirror(store: store).upsert(job)
            if let meeting = store.meeting(meetingID), job.status.isActive, !meeting.activeJobTypes.contains(type.rawValue) {
                meeting.activeJobTypes.append(type.rawValue)
            }
            finish(key: key, store: store)
            return .done
        } catch let error as APIError {
            switch error {
            case .alreadyTranscribed, .jobInProgress:
                finish(key: key, store: store)
                return .done
            case .audioNotReady:
                return .retryLater(error)
            case .quotaExceeded, .entitlementInactive:
                guard let current = store.intent(key: key) else { return .waiting(error.code) }
                current.lastErrorCode = error.code
                if type == .transcribe, let meeting = store.meeting(meetingID) {
                    meeting.localPhase = error.code == "entitlement_inactive" ? .waitingEntitlement : .waitingQuota
                }
                if !current.quotaBlockedLogged {
                    current.quotaBlockedLogged = true
                    env.app.events.log(.quotaBlocked, meetingID: meetingID)
                }
                store.save()
                return .waiting(error.code)
            case .idempotencyKeyReused:
                if let current = store.intent(key: key) {
                    current.idempotencyKey = UUID()
                    current.keySent = false
                    store.save()
                }
                return .retryLater(error)
            case .notFound:
                return .missing
            case .audioUnavailable:
                if let meeting = store.meeting(meetingID) {
                    meeting.localPhase = .rejected
                    meeting.rejectCode = error.code
                }
                finish(key: key, store: store, resetPhase: false)
                return .rejected(error)
            case .unauthenticated, .offline:
                return .retryLater(error)
            default:
                if error.isAmbiguous || error == .uploadBusy { return .retryLater(error) }
                if case .decoding = error { return .retryLater(error) }
                finish(key: key, store: store, resetPhase: false)
                return .rejected(error)
            }
        } catch {
            return .retryLater(nil)
        }
    }

    private static func finish(key: UUID, store: SyncStore, resetPhase: Bool = true) {
        guard let current = store.intent(key: key) else { return }
        let meetingID = current.meetingID, type = current.type
        store.context.delete(current)
        if resetPhase, type == .transcribe, let meeting = store.meeting(meetingID),
           [.waitingQuota, .waitingEntitlement, .queued, .uploading, .verifying, .waitingConnection].contains(meeting.localPhase) {
            meeting.localPhase = .none
        }
        store.save()
    }

    static func language(of intent: PendingIntent) -> TranscriptionLanguage? {
        (try? APICoding.decoder().decode(TranscriptionRequest.self, from: intent.body))?.language
    }
}
