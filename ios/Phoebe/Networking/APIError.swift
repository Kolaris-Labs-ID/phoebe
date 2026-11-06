import Foundation

struct FieldError: Equatable, Sendable {
    let field: String
    let reason: String
}

enum APIError: Error, Equatable, Sendable {
    case invalidJSON
    case missingIdempotencyKey
    case unauthenticated
    case quotaExceeded(requiredSeconds: Int?, remainingSeconds: Int?)
    case entitlementInactive(periodEnd: Date?)
    case invalidSignature
    case notFound
    case revisionConflict(currentRevisionNumber: Int, currentRevisionId: UUID?)
    case jobInProgress(jobId: UUID?)
    case alreadyTranscribed
    case audioExists
    case audioNotReady
    case transcriptNotReady
    case aiNotesNotReady
    case uploadOffsetMismatch(receivedBytes: Int)
    case uploadBusy
    case uploadNotOpen(status: String?)
    case uploadIncomplete(receivedBytes: Int?, sizeBytes: Int?)
    case nameTaken
    case meetingIdConflict
    case idConflict
    case metadataConflict(currentMetadataRevision: Int)
    case uploadExpired
    case audioUnavailable
    case urlExpired
    case payloadTooLarge
    case chunkTooLarge
    case uploadTooLarge
    case validationFailed([FieldError])
    case invalidContent(path: String?, reason: String?)
    case idempotencyKeyReused
    case checksumMismatch
    case audioUnsupported
    case audioCorrupt
    case audioEmpty
    case audioTooLong(durationMs: Int?, maxDurationMs: Int?)
    case rateLimited(retryAfterSeconds: Int?)
    case `internal`(requestId: String?)
    case unavailable
    case server(status: Int, code: String?)
    case offline
    case transport(String?)
    case timeout
    case decoding(String)

    // Ambiguous: the server may have committed; retry the identical request (same key / mutation id).
    var isAmbiguous: Bool {
        switch self {
        case .transport, .timeout, .internal, .unavailable: true
        case .server(let status, _): status >= 500
        default: false
        }
    }

    var isRetryableTransport: Bool { isAmbiguous || self == .offline || self == .uploadBusy }

    var code: String {
        switch self {
        case .invalidJSON: "invalid_json"
        case .missingIdempotencyKey: "missing_idempotency_key"
        case .unauthenticated: "unauthenticated"
        case .quotaExceeded: "quota_exceeded"
        case .entitlementInactive: "entitlement_inactive"
        case .invalidSignature: "invalid_signature"
        case .notFound: "not_found"
        case .revisionConflict: "revision_conflict"
        case .jobInProgress: "job_in_progress"
        case .alreadyTranscribed: "already_transcribed"
        case .audioExists: "audio_exists"
        case .audioNotReady: "audio_not_ready"
        case .transcriptNotReady: "transcript_not_ready"
        case .aiNotesNotReady: "ai_notes_not_ready"
        case .uploadOffsetMismatch: "upload_offset_mismatch"
        case .uploadBusy: "upload_busy"
        case .uploadNotOpen: "upload_not_open"
        case .uploadIncomplete: "upload_incomplete"
        case .nameTaken: "name_taken"
        case .meetingIdConflict: "meeting_id_conflict"
        case .idConflict: "id_conflict"
        case .metadataConflict: "metadata_conflict"
        case .uploadExpired: "upload_expired"
        case .audioUnavailable: "audio_unavailable"
        case .urlExpired: "url_expired"
        case .payloadTooLarge: "payload_too_large"
        case .chunkTooLarge: "chunk_too_large"
        case .uploadTooLarge: "upload_too_large"
        case .validationFailed: "validation_failed"
        case .invalidContent: "invalid_content"
        case .idempotencyKeyReused: "idempotency_key_reused"
        case .checksumMismatch: "checksum_mismatch"
        case .audioUnsupported: "audio_unsupported"
        case .audioCorrupt: "audio_corrupt"
        case .audioEmpty: "audio_empty"
        case .audioTooLong: "audio_too_long"
        case .rateLimited: "rate_limited"
        case .internal: "internal"
        case .unavailable: "unavailable"
        case .server(_, let code): code ?? "server"
        case .offline: "offline"
        case .transport: "transport"
        case .timeout: "timeout"
        case .decoding: "decoding"
        }
    }

    static func from(_ error: Error) -> APIError {
        if let api = error as? APIError { return api }
        guard let url = error as? URLError else { return .transport(String(describing: type(of: error))) }
        switch url.code {
        case .notConnectedToInternet, .dataNotAllowed, .internationalRoamingOff: return .offline
        case .timedOut: return .timeout
        default: return .transport("URLError \(url.code.rawValue)")
        }
    }

    static func from(status: Int, data: Data, retryAfter: String? = nil) -> APIError {
        let root = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        let error = root?["error"] as? [String: Any]
        let code = error?["code"] as? String
        let d = error?["details"] as? [String: Any] ?? [:]
        let requestID = error?["request_id"] as? String
        func int(_ key: String) -> Int? { (d[key] as? NSNumber)?.intValue }
        func uuid(_ key: String) -> UUID? { (d[key] as? String).flatMap(UUID.init(uuidString:)) }

        switch code {
        case "invalid_json": return .invalidJSON
        case "missing_idempotency_key": return .missingIdempotencyKey
        case "unauthenticated": return .unauthenticated
        case "quota_exceeded": return .quotaExceeded(requiredSeconds: int("required_seconds"), remainingSeconds: int("remaining_seconds"))
        case "entitlement_inactive": return .entitlementInactive(periodEnd: (d["period_end"] as? String).flatMap(APICoding.parseDate))
        case "invalid_signature": return .invalidSignature
        case "not_found": return .notFound
        case "revision_conflict": return .revisionConflict(currentRevisionNumber: int("current_revision_number") ?? 0, currentRevisionId: uuid("current_revision_id"))
        case "job_in_progress": return .jobInProgress(jobId: uuid("job_id"))
        case "already_transcribed": return .alreadyTranscribed
        case "audio_exists": return .audioExists
        case "audio_not_ready": return .audioNotReady
        case "transcript_not_ready": return .transcriptNotReady
        case "ai_notes_not_ready": return .aiNotesNotReady
        case "upload_offset_mismatch": return .uploadOffsetMismatch(receivedBytes: int("received_bytes") ?? 0)
        case "upload_busy": return .uploadBusy
        case "upload_not_open": return .uploadNotOpen(status: d["status"] as? String)
        case "upload_incomplete": return .uploadIncomplete(receivedBytes: int("received_bytes"), sizeBytes: int("size_bytes"))
        case "name_taken": return .nameTaken
        case "meeting_id_conflict": return .meetingIdConflict
        case "id_conflict": return .idConflict
        case "metadata_conflict": return .metadataConflict(currentMetadataRevision: int("current_metadata_revision") ?? 0)
        case "upload_expired": return .uploadExpired
        case "audio_unavailable": return .audioUnavailable
        case "url_expired": return .urlExpired
        case "payload_too_large": return .payloadTooLarge
        case "chunk_too_large": return .chunkTooLarge
        case "upload_too_large": return .uploadTooLarge
        case "validation_failed":
            let fields = (d["fields"] as? [[String: Any]] ?? []).compactMap { f -> FieldError? in
                guard let field = f["field"] as? String, let reason = f["reason"] as? String else { return nil }
                return FieldError(field: field, reason: reason)
            }
            return .validationFailed(fields)
        case "invalid_content": return .invalidContent(path: d["path"] as? String, reason: d["reason"] as? String)
        case "idempotency_key_reused": return .idempotencyKeyReused
        case "checksum_mismatch": return .checksumMismatch
        case "audio_unsupported": return .audioUnsupported
        case "audio_corrupt": return .audioCorrupt
        case "audio_empty": return .audioEmpty
        case "audio_too_long": return .audioTooLong(durationMs: int("duration_ms"), maxDurationMs: int("max_duration_ms"))
        case "rate_limited": return .rateLimited(retryAfterSeconds: int("retry_after_seconds") ?? retryAfter.flatMap { Int($0) })
        case "internal": return .internal(requestId: requestID)
        case "unavailable": return .unavailable
        default:
            switch status {
            case 401: return .unauthenticated
            case 404: return .notFound
            case 500: return .internal(requestId: requestID)
            case 503: return .unavailable
            default: return .server(status: status, code: code)
            }
        }
    }
}
