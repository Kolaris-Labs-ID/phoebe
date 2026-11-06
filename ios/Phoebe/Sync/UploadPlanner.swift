import Foundation

// Pure decisions for the upload protocol (api §4.4, ios.md §8); UploadManager executes them.
enum UploadPlanner {
    struct Chunk: Equatable, Sendable {
        let offset: Int
        let length: Int
        let usesSourceFile: Bool
    }

    enum Stage: Sendable {
        case createMeeting, createSession, refreshSession, putChunk, complete
    }

    indirect enum Action: Equatable, Sendable {
        case createSession
        case refreshSession
        case uploadChunks
        case resume(at: Int)
        case complete
        case pollVerifying
        case checkServerAudio
        case restartAfterChecksum
        case backoff(then: Action)
        case busy(then: Action)
        case reject(String)
        case fail(String)
        case orphan
        case confirmMissing
        case waitForConnection
        case stop
    }

    static let maxChecksumRestarts = 1

    static func nextChunk(sizeBytes: Int, receivedBytes: Int, chunkMaxBytes: Int) -> Chunk? {
        let remaining = sizeBytes - receivedBytes
        guard remaining > 0, chunkMaxBytes > 0 else { return nil }
        let length = min(chunkMaxBytes, remaining)
        return Chunk(offset: receivedBytes, length: length, usesSourceFile: receivedBytes == 0 && length == sizeBytes)
    }

    static func next(after session: UploadSessionDTO, checksumRestarts: Int) -> Action {
        switch session.status {
        case .open: session.receivedBytes >= session.sizeBytes ? .complete : .uploadChunks
        case .verifying: .pollVerifying
        case .completed: .complete
        case .failed:
            switch session.failureReason {
            case "checksum_mismatch"?: checksumRestarts < maxChecksumRestarts ? .restartAfterChecksum : .fail("checksum_mismatch")
            case let reason?: .reject(reason)
            case nil: .fail("upload_failed")
            }
        case .expired, .canceled: .createSession
        }
    }

    static func next(after error: APIError, stage: Stage, checksumRestarts: Int) -> Action {
        switch error {
        case .unauthenticated: return .stop
        // A chunk task outlived its bearer: the session GET refreshes the token, then the chunk is sent again.
        case .accessTokenExpired: return .refreshSession
        case .offline: return .waitForConnection
        case .meetingIdConflict: return .orphan
        case .checksumMismatch:
            return checksumRestarts < maxChecksumRestarts ? .restartAfterChecksum : .fail(error.code)
        case .audioUnsupported, .audioCorrupt, .audioEmpty, .audioTooLong, .audioUnavailable, .validationFailed:
            return .reject(error.code)
        default: break
        }
        if error.isAmbiguous {
            return .backoff(then: stage == .createSession || stage == .createMeeting ? retry(stage) : .refreshSession)
        }
        if case .decoding = error { return .backoff(then: .refreshSession) }

        switch stage {
        case .createMeeting:
            return error == .notFound ? .confirmMissing : .fail(error.code)
        case .createSession:
            switch error {
            case .audioExists: return .checkServerAudio
            case .uploadBusy: return .busy(then: .createSession)
            case .uploadTooLarge: return .reject(error.code)
            case .notFound: return .confirmMissing
            default: return .fail(error.code)
            }
        case .refreshSession:
            return error == .notFound ? .createSession : .backoff(then: .refreshSession)
        case .putChunk:
            switch error {
            case .uploadOffsetMismatch(let received): return .resume(at: received)
            case .uploadBusy: return .busy(then: .refreshSession)
            case .uploadNotOpen(let status): return status == UploadStatus.completed.rawValue ? .complete : .refreshSession
            case .uploadExpired, .notFound: return .createSession
            case .uploadTooLarge: return .refreshSession
            default: return .fail(error.code)
            }
        case .complete:
            switch error {
            case .uploadIncomplete(let received, _): return received.map { .resume(at: $0) } ?? .refreshSession
            case .uploadBusy: return .pollVerifying
            case .uploadNotOpen: return .refreshSession
            case .uploadExpired, .notFound: return .createSession
            default: return .fail(error.code)
            }
        }
    }

    private static func retry(_ stage: Stage) -> Action {
        switch stage {
        case .createSession: .createSession
        default: .refreshSession
        }
    }
}
