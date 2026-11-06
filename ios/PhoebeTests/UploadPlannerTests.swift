import Foundation
import Testing
@testable import Phoebe

struct UploadPlannerTests {
    typealias P = UploadPlanner

    // MARK: Chunking

    @Test func chunkIsMinOfMaxAndRemaining() {
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 0, chunkMaxBytes: 40) == P.Chunk(offset: 0, length: 40, usesSourceFile: false))
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 80, chunkMaxBytes: 40) == P.Chunk(offset: 80, length: 20, usesSourceFile: false))
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 100, chunkMaxBytes: 40) == nil)
    }

    @Test func singleChunkFileUploadsFromSource() {
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 0, chunkMaxBytes: 100)?.usesSourceFile == true)
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 0, chunkMaxBytes: 33_554_432)?.usesSourceFile == true)
        #expect(P.nextChunk(sizeBytes: 100, receivedBytes: 10, chunkMaxBytes: 33_554_432)?.usesSourceFile == false)
    }

    // MARK: Error branches (api §4.4)

    @Test func offsetMismatchResumesAtServerReceivedBytes() {
        #expect(P.next(after: .uploadOffsetMismatch(receivedBytes: 512), stage: .putChunk, checksumRestarts: 0) == .resume(at: 512))
        #expect(P.next(after: .uploadIncomplete(receivedBytes: 7, sizeBytes: 10), stage: .complete, checksumRestarts: 0) == .resume(at: 7))
    }

    @Test func checksumMismatchRestartsOnceThenFails() {
        #expect(P.next(after: .checksumMismatch, stage: .putChunk, checksumRestarts: 0) == .restartAfterChecksum)
        #expect(P.next(after: .checksumMismatch, stage: .complete, checksumRestarts: 1) == .fail("checksum_mismatch"))
    }

    @Test(arguments: [
        APIError.audioUnsupported, .audioCorrupt, .audioEmpty, .audioTooLong(durationMs: 1, maxDurationMs: 0),
    ])
    func audioErrorsReject(_ error: APIError) {
        #expect(P.next(after: error, stage: .putChunk, checksumRestarts: 0) == .reject(error.code))
        #expect(P.next(after: error, stage: .complete, checksumRestarts: 0) == .reject(error.code))
        #expect(P.next(after: error, stage: .createSession, checksumRestarts: 0) == .reject(error.code))
    }

    @Test func uploadNotOpenCompletedCompletesOtherwiseRefreshes() {
        #expect(P.next(after: .uploadNotOpen(status: "completed"), stage: .putChunk, checksumRestarts: 0) == .complete)
        #expect(P.next(after: .uploadNotOpen(status: "failed"), stage: .putChunk, checksumRestarts: 0) == .refreshSession)
        #expect(P.next(after: .uploadNotOpen(status: "canceled"), stage: .complete, checksumRestarts: 0) == .refreshSession)
    }

    @Test func busyExpiredAndExistingAudio() {
        #expect(P.next(after: .uploadBusy, stage: .putChunk, checksumRestarts: 0) == .busy(then: .refreshSession))
        #expect(P.next(after: .uploadBusy, stage: .createSession, checksumRestarts: 0) == .busy(then: .createSession))
        #expect(P.next(after: .uploadBusy, stage: .complete, checksumRestarts: 0) == .pollVerifying)
        #expect(P.next(after: .uploadExpired, stage: .putChunk, checksumRestarts: 0) == .createSession)
        #expect(P.next(after: .uploadExpired, stage: .complete, checksumRestarts: 0) == .createSession)
        #expect(P.next(after: .audioExists, stage: .createSession, checksumRestarts: 0) == .checkServerAudio)
        #expect(P.next(after: .uploadTooLarge, stage: .createSession, checksumRestarts: 0) == .reject("upload_too_large"))
    }

    @Test func meetingAndSessionLookupFailures() {
        #expect(P.next(after: .meetingIdConflict, stage: .createMeeting, checksumRestarts: 0) == .orphan)
        #expect(P.next(after: .notFound, stage: .createSession, checksumRestarts: 0) == .confirmMissing)
        #expect(P.next(after: .notFound, stage: .refreshSession, checksumRestarts: 0) == .createSession)
    }

    @Test func ambiguousOfflineAndUnauthenticated() {
        #expect(P.next(after: .transport(nil), stage: .putChunk, checksumRestarts: 0) == .backoff(then: .refreshSession))
        #expect(P.next(after: .timeout, stage: .complete, checksumRestarts: 0) == .backoff(then: .refreshSession))
        #expect(P.next(after: .internal(requestId: nil), stage: .createSession, checksumRestarts: 0) == .backoff(then: .createSession))
        #expect(P.next(after: .offline, stage: .putChunk, checksumRestarts: 0) == .waitForConnection)
        #expect(P.next(after: .unauthenticated, stage: .putChunk, checksumRestarts: 0) == .stop)
    }

    @Test func expiredBearerOnAChunkReschedulesThroughTheSessionGet() {
        #expect(P.next(after: .accessTokenExpired, stage: .putChunk, checksumRestarts: 0) == .refreshSession)
    }

    @Test func sessionStatusBranches() {
        let id = UUID(), meeting = UUID()
        func s(_ status: String, received: Int = 0, failure: String? = nil) -> UploadSessionDTO {
            try! APICoding.decoder().decode(UploadSessionDTO.self, from: Data(Fx.session(id: id, meetingID: meeting, status: status, size: 10, received: received, failure: failure).utf8))
        }
        #expect(P.next(after: s("open", received: 4), checksumRestarts: 0) == .uploadChunks)
        #expect(P.next(after: s("open", received: 10), checksumRestarts: 0) == .complete)
        #expect(P.next(after: s("verifying", received: 10), checksumRestarts: 0) == .pollVerifying)
        #expect(P.next(after: s("completed", received: 10), checksumRestarts: 0) == .complete)
        #expect(P.next(after: s("failed", failure: "checksum_mismatch"), checksumRestarts: 0) == .restartAfterChecksum)
        #expect(P.next(after: s("failed", failure: "checksum_mismatch"), checksumRestarts: 1) == .fail("checksum_mismatch"))
        #expect(P.next(after: s("failed", failure: "audio_corrupt"), checksumRestarts: 0) == .reject("audio_corrupt"))
        #expect(P.next(after: s("expired"), checksumRestarts: 0) == .createSession)
        #expect(P.next(after: s("canceled"), checksumRestarts: 0) == .createSession)
    }
}

// End-to-end through UploadManager with a stubbed server and foreground transport.
@MainActor
struct UploadManagerFlowTests {
    @MainActor struct Setup {
        let h: SyncHarness
        let meeting: Meeting
        let upload: PendingUpload
        let intent: PendingIntent
        let transport: StubChunkTransport
        let manager: UploadManager
        let bytes: Data
        var uploadID: UUID
        var createPath: String { "/v1/meetings/\(meeting.id.lower)/uploads" }
        var chunkPath: String { "/v1/uploads/\(uploadID.lower)" }
        var transcriptionPath: String { "/v1/meetings/\(meeting.id.lower)/transcription" }

        func session(_ status: String = "open", received: Int = 0, chunkMax: Int = 33_554_432) -> String {
            Fx.session(id: uploadID, meetingID: meeting.id, status: status, size: bytes.count, received: received, chunkMax: chunkMax)
        }

        func run() async {
            await manager.kick(meetingID: meeting.id)
            await manager.waitUntilIdle()
        }
    }

    func setup(serverCreated: Bool = true, size: Int = 10) async throws -> Setup {
        let h = try SyncHarness()
        await h.loadConfig()
        let meeting = h.seedMeeting(serverCreated: serverCreated)
        meeting.localPhase = .queued
        let bytes = Data((0..<size).map { UInt8($0 % 251) })
        let upload = try h.seedUpload(meetingID: meeting.id, bytes: bytes)
        let intent = h.seedIntent(meetingID: meeting.id, language: .id)
        let transport = StubChunkTransport(session: h.session)
        return Setup(h: h, meeting: meeting, upload: upload, intent: intent, transport: transport, manager: h.uploads(transport: transport), bytes: bytes, uploadID: UUID())
    }

    @Test func singleChunkUploadsSourceFileAndLastChunkJobDeletesIntent() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session("completed", received: 10), audio: true, job: Fx.job(meetingID: s.meeting.id))))
        await s.run()

        let create = try #require(s.h.server.requests(.post, s.createPath).first).json
        let transcription = try #require(create["transcription"] as? [String: Any])
        #expect(transcription["idempotency_key"] as? String == s.intent.idempotencyKey.lower)
        #expect(transcription["language"] as? String == "id")
        #expect(s.transport.sent.count == 1)
        #expect(s.transport.sent.first?.file.path == s.upload.filePath)
        #expect(s.transport.sent.first?.offset == "0")
        #expect(s.upload.state == .verified)
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
        #expect(s.h.server.requests(.post, "/transcription").isEmpty)
        #expect(s.meeting.localPhase == LocalPhase.none)
        #expect(s.meeting.activeJobTypes == ["transcribe"])
    }

    @Test func chunkAccessTokenExpiredRefreshesAndReschedulesTheChunk() async throws {
        let s = try await setup()
        s.h.app.auth.saveTokens(.pair("at_1", expiresIn: 900))
        defer { s.h.app.auth.clearSession() }
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .error(401, "access_token_expired"))
        s.h.server.enqueue("GET", s.chunkPath, .error(401, "access_token_expired"))
        s.h.server.enqueue("POST", "/v1/auth/refresh", .json(200, AuthServer.pairJSON(2)))
        s.h.server.enqueue("GET", s.chunkPath, .json(200, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session("completed", received: 10), audio: true, job: Fx.job(meetingID: s.meeting.id))))
        await s.run()

        #expect(s.h.server.requests(.post, "/v1/auth/refresh").count == 1)
        #expect(s.h.server.requests(.put, s.chunkPath).map { $0.headers["Authorization"] } == ["Bearer at_1", "Bearer at_2"])
        #expect(s.transport.sent.map(\.offset) == ["0", "0"])
        #expect(s.h.server.requests(.post, s.createPath).count == 1)
        #expect(s.h.server.requests(.post, "/v1/meetings").filter { $0.path == "/v1/meetings" }.isEmpty)
        #expect(s.upload.state == .verified)
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
        #expect(s.h.app.auth.tokens?.refreshToken == "rt_2")
    }

    @Test func chunkTaskIsCreatedWithATokenValidForFiveMinutes() async throws {
        let s = try await setup()
        s.h.app.auth.saveTokens(.pair("at_1", expiresIn: 120))
        defer { s.h.app.auth.clearSession() }
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("POST", "/v1/auth/refresh", .json(200, AuthServer.pairJSON(2)))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session("completed", received: 10), audio: true, job: Fx.job(meetingID: s.meeting.id))))
        await s.run()

        #expect(s.h.server.requests.map(\.path).filter { $0 != "/v1/config" } == [s.createPath, "/v1/auth/refresh", s.chunkPath])
        #expect(s.h.server.requests(.post, s.createPath).first?.headers["Authorization"] == "Bearer at_1")
        #expect(s.h.server.requests(.put, s.chunkPath).first?.headers["Authorization"] == "Bearer at_2")
        #expect(s.upload.state == .verified)
    }

    @Test func offsetMismatchResumesFromReceivedBytesAndStagesChunks() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session(chunkMax: 4))))
        s.h.server.enqueue("PUT", s.chunkPath, .error(409, "upload_offset_mismatch", #"{"received_bytes":4}"#))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session(received: 8, chunkMax: 4), audio: false)))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session("completed", received: 10, chunkMax: 4), audio: true)))
        s.h.server.enqueue("POST", s.transcriptionPath, .json(202, Fx.jobEnvelope(meetingID: s.meeting.id)))
        await s.run()

        #expect(s.transport.sent.map(\.offset) == ["0", "4", "8"])
        #expect(s.transport.sent[1].bytes == s.bytes[4..<8])
        #expect(s.transport.sent[2].bytes == s.bytes[8..<10])
        #expect(s.transport.sent.allSatisfy { $0.file.path != s.upload.filePath })
        #expect(s.upload.state == .verified)
        let start = try #require(s.h.server.requests(.post, "/transcription").first)
        #expect(start.headers["Idempotency-Key"] == s.intent.idempotencyKey.lower)
        #expect(start.body == APICoding.encode(TranscriptionRequest(language: .id)))
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
    }

    @Test func checksumMismatchRestartsOnceThenFails() async throws {
        let s = try await setup()
        s.h.server.always("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.always("PUT", s.chunkPath, .error(422, "checksum_mismatch"))
        await s.run()

        #expect(s.h.server.requests(.post, s.createPath).count == 2)
        #expect(s.h.server.requests(.put, s.chunkPath).count == 2)
        #expect(s.upload.state == .failed)
        #expect(s.upload.rejectCode == "checksum_mismatch")
        #expect(s.meeting.localPhase == .rejected)
        #expect(s.h.server.requests(.post, "/transcription").isEmpty)
    }

    @Test func audioErrorRejectsUpload() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .error(422, "audio_corrupt"))
        await s.run()

        #expect(s.upload.state == .rejected)
        #expect(s.meeting.rejectCode == "audio_corrupt")
        #expect(s.meeting.localPhase == .rejected)
        #expect(s.h.server.requests.filter { $0.path == s.createPath }.count == 1)
    }

    @Test func uploadNotOpenCompletedCallsComplete() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .error(409, "upload_not_open", #"{"status":"completed"}"#))
        s.h.server.enqueue("POST", s.chunkPath + "/complete", .json(200, Fx.uploadResult(s.session("completed", received: 10), audio: true, job: Fx.job(meetingID: s.meeting.id))))
        await s.run()

        #expect(s.h.server.requests(.post, "/complete").count == 1)
        #expect(s.upload.state == .verified)
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
    }

    @Test func audioExistsWithServerAudioVerifiesAndContinuesToIntent() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .error(409, "audio_exists"))
        s.h.server.enqueue("GET", "/v1/meetings/\(s.meeting.id.lower)", .json(200, Fx.meeting(id: s.meeting.id, audio: true)))
        s.h.server.enqueue("POST", s.transcriptionPath, .json(202, Fx.jobEnvelope(meetingID: s.meeting.id)))
        await s.run()

        #expect(s.transport.sent.isEmpty)
        #expect(s.upload.state == .verified)
        #expect(s.h.server.requests(.post, "/transcription").count == 1)
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
    }

    @Test func alreadyTranscribedCountsAsSuccess() async throws {
        let s = try await setup()
        s.upload.state = .verified
        s.h.store.save()
        s.h.server.enqueue("POST", s.transcriptionPath, .error(409, "already_transcribed"))
        await s.manager.resumeAll()
        await s.manager.waitUntilIdle()

        #expect(s.h.server.requests.map(\.path).filter { $0 != "/v1/config" } == [s.transcriptionPath])
        #expect(s.h.store.intents(s.meeting.id).isEmpty)
        #expect(s.meeting.localPhase == LocalPhase.none)
    }

    @Test func meetingIdConflictOrphansWithoutLoop() async throws {
        let s = try await setup(serverCreated: false)
        s.h.server.always("POST", "/v1/meetings", .error(409, "meeting_id_conflict"))
        await s.run()
        await s.run()

        #expect(s.h.server.requests.map(\.path).filter { $0 != "/v1/config" } == ["/v1/meetings"])
        #expect(s.meeting.localPhase == .orphaned)
        #expect(s.transport.sent.isEmpty)
    }

    @Test func transcribeIntentIsNotSentBeforeVerified() async throws {
        let s = try await setup()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .error(503, "unavailable"))
        s.h.server.enqueue("GET", s.chunkPath, .json(200, Fx.uploadEnvelope(Fx.session(id: s.uploadID, meetingID: s.meeting.id, status: "failed", size: 10, received: 10, failure: "audio_empty"))))
        await s.run()

        let engine = s.h.engine()
        s.upload.state = .uploading
        s.h.store.save()
        await engine.pushAll()

        #expect(s.h.server.requests(.post, "/transcription").isEmpty)
        #expect(s.h.store.intents(s.meeting.id).count == 1)
        #expect(s.meeting.rejectCode == "audio_empty")
    }

    @Test func nothingStartsBeforeConfigIsFetched() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        _ = try h.seedUpload(meetingID: meeting.id, bytes: Data(count: 4))
        let manager = h.uploads()
        await manager.resumeAll()
        await manager.kick(meetingID: meeting.id)
        await manager.waitUntilIdle()
        #expect(h.server.requests.isEmpty)
    }

    @Test func offlineMarksWaitingForConnection() async throws {
        let s = try await setup()
        s.h.app.connectivity.update(false)
        await s.run()
        #expect(s.h.server.requests.allSatisfy { $0.path == "/v1/config" })
        #expect(s.meeting.localPhase == .waitingConnection)
    }

    @Test func uploadCreateCarriesTheIntentNotesMode() async throws {
        let s = try await setup()
        s.intent.body = APICoding.encode(TranscriptionRequest(language: .id, notes: .skip))
        s.h.store.save()
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, Fx.uploadResult(s.session("completed", received: 10), audio: true, job: Fx.job(meetingID: s.meeting.id))))
        await s.run()

        let create = try #require(s.h.server.requests(.post, s.createPath).first).json
        let transcription = try #require(create["transcription"] as? [String: Any])
        #expect(transcription["notes"] as? String == "skip")
    }

    @Test func blockedAutoStartParksTheIntentWithoutPostingTranscription() async throws {
        let s = try await setup()
        let blocked = Fx.uploadResult(s.session("completed", received: 10), audio: true)
            .replacingOccurrences(of: #""job":null}"#, with: #""job":null,"transcription_blocked":{"code":"ai_generation_limit","next_slot_at":"2026-11-06T05:00:00.000Z"}}"#)
        s.h.server.enqueue("POST", s.createPath, .json(201, Fx.uploadEnvelope(s.session())))
        s.h.server.enqueue("PUT", s.chunkPath, .json(200, blocked))
        await s.run()
        await s.manager.resumeAll()
        await s.manager.waitUntilIdle()

        #expect(s.upload.state == .verified)
        #expect(IntentSender.isBlockedByAILimit(s.intent))
        #expect(s.h.store.intents(s.meeting.id).count == 1)
        #expect(s.h.server.requests(.post, "/transcription").isEmpty)
        #expect(s.meeting.activeJobTypes.isEmpty)
    }

    @Test func quotaExceededWaitsAndLogsOnce() async throws {
        let s = try await setup()
        s.upload.state = .verified
        s.h.store.save()
        s.h.server.always("POST", s.transcriptionPath, .error(402, "quota_exceeded", #"{"required_seconds":60,"remaining_seconds":0}"#))
        await s.manager.resumeAll()
        await s.manager.waitUntilIdle()
        await s.manager.resumeAll()
        await s.manager.waitUntilIdle()

        #expect(s.h.server.requests(.post, "/transcription").count == 1)
        #expect(s.meeting.localPhase == .waitingQuota)
        #expect(s.intent.lastErrorCode == "quota_exceeded")
        #expect(s.intent.quotaBlockedLogged)
    }
}
