import Foundation
import Testing
@testable import Phoebe

struct StubResponse {
    var status: Int
    var body: String
    var headers: [String: String] = [:]
    var error: URLError?
}

final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    struct Captured {
        let request: URLRequest
        let body: Data
    }

    nonisolated(unsafe) static var responses: [StubResponse] = []
    nonisolated(unsafe) static var captured: [Captured] = []

    static func reset(_ responses: [StubResponse]) {
        self.responses = responses
        captured = []
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.captured.append(Captured(request: request, body: Self.readBody(request)))
        let stub = Self.responses.isEmpty ? StubResponse(status: 500, body: "") : Self.responses.removeFirst()
        if let error = stub.error {
            client?.urlProtocol(self, didFailWithError: error)
            return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: stub.status, httpVersion: "HTTP/1.1", headerFields: stub.headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(stub.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func readBody(_ request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}

final class Flag: @unchecked Sendable {
    var value = false
}

@Suite(.serialized)
struct APIClientTests {
    let unauthenticated = Flag()

    func makeClient() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        let flag = unauthenticated
        return APIClient(
            baseURL: URL(string: "http://localhost:8080")!,
            session: URLSession(configuration: config),
            tokens: MemoryTokenStore(.pair("phb_test", expiresIn: 900)),
            onUnauthenticated: { flag.value = true }
        )
    }

    static func envelope(_ code: String, _ details: String = "{}") -> String {
        "{\"error\":{\"code\":\"\(code)\",\"message\":\"m\",\"details\":\(details),\"request_id\":\"rid-1\"}}"
    }

    static let jobJSON = """
    {"job":{"id":"0b6d1f0e-1111-4000-8000-000000000001","meeting_id":"0b6d1f0e-1111-4000-8000-000000000002","type":"transcribe","trigger":"user",\
    "status":"queued","attempts":0,"max_attempts":3,"error_category":null,"result":null,"created_at":"2026-11-06T02:00:00.000Z","started_at":null,"finished_at":null}}
    """

    // MARK: snake_case + dates

    @Test func snakeCaseRoundTripOfSegmentAndNoteIds() throws {
        let refs: [SourceRef] = [.transcript(segmentId: "s42", startMs: 734000, endMs: 741500), .userNotes(noteId: "n3")]
        let json = Fixtures.string(try APICoding.encoder().encode(refs))
        #expect(json == "[{\"approximate\":false,\"end_ms\":741500,\"segment_id\":\"s42\",\"start_ms\":734000,\"type\":\"transcript\"},{\"note_id\":\"n3\",\"type\":\"user_notes\"}]")
        #expect(try APICoding.decoder().decode([SourceRef].self, from: Data(json.utf8)) == refs)

        let segment = try APICoding.decoder().decode(TranscriptSegment.self, from: Data("{\"id\":\"s1\",\"start_ms\":0,\"end_ms\":4200,\"text\":\"Halo\"}".utf8))
        #expect(segment.endMs == 4200)
    }

    @Test func fractionalSecondDates() throws {
        let user = try APICoding.decoder().decode(UserDTO.self, from: Data("""
        {"id":"0B6D1F0E-1111-4000-8000-000000000001","display_name":"Ari","language":"id","timezone":"Asia/Jakarta","created_at":"2026-11-06T02:00:00.123Z"}
        """.utf8))
        #expect(APICoding.formatDate(user.createdAt) == "2026-11-06T02:00:00.123Z")
        #expect(APICoding.parseDate("2026-11-06T02:00:00Z").map(APICoding.formatDate) == "2026-11-06T02:00:00.000Z")
        let encoded = Fixtures.string(try APICoding.encoder().encode(["at": user.createdAt]))
        #expect(encoded == "{\"at\":\"2026-11-06T02:00:00.123Z\"}")
    }

    @Test func decodesMeetingWithNulls() async throws {
        StubURLProtocol.reset([StubResponse(status: 200, body: """
        {"meeting":{"id":"0b6d1f0e-1111-4000-8000-000000000002","source":"import","title":"","started_at":null,"timezone":"Asia/Jakarta",\
        "language":"auto","participants":[],"metadata_revision":1,"folder_id":null,"tag_ids":[],"pipeline_state":"awaiting_audio",\
        "created_at":"2026-11-06T02:00:00.000Z","updated_at":"2026-11-06T02:00:00.000Z","audio":null,"upload":null,"transcript":null,\
        "documents":{"manual_notes":null,"ai_notes":null,"mom":null},"jobs":{"transcribe":null,"generate_notes":null,"generate_mom":null}}}
        """)])
        let meeting = try await makeClient().send(Endpoints.meeting(UUID())).meeting
        #expect(meeting.source == .import)
        #expect(meeting.pipelineState == .awaitingAudio)
        #expect(meeting.documents[.mom] == nil)
        let request = try #require(StubURLProtocol.captured.first?.request)
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer phb_test")
        #expect(request.value(forHTTPHeaderField: "X-Request-ID") != nil)
    }

    // MARK: error envelope

    @Test func unauthenticatedMapsAndNotifies() async {
        StubURLProtocol.reset([StubResponse(status: 401, body: Self.envelope("unauthenticated"))])
        await #expect(throws: APIError.unauthenticated) { try await makeClient().send(Endpoints.me()) }
        #expect(unauthenticated.value)
    }

    @Test func sessionRevokedAndExpiredSignOut() async {
        StubURLProtocol.reset([
            StubResponse(status: 401, body: Self.envelope("session_revoked")),
            StubResponse(status: 401, body: Self.envelope("session_expired")),
        ])
        let client = makeClient()
        await #expect(throws: APIError.unauthenticated) { try await client.send(Endpoints.me()) }
        await #expect(throws: APIError.unauthenticated) { try await client.send(Endpoints.usage()) }
        #expect(unauthenticated.value)
    }

    @Test func accessTokenExpiredMapsToItsOwnCode() {
        let error = APIError.from(status: 401, data: Data(Self.envelope("access_token_expired").utf8))
        #expect(error == .accessTokenExpired)
        #expect(error.code == "access_token_expired")
    }

    @Test func paymentRequiredCodes() async {
        StubURLProtocol.reset([
            StubResponse(status: 402, body: Self.envelope("quota_exceeded", "{\"required_seconds\":3601,\"remaining_seconds\":12}")),
            StubResponse(status: 402, body: Self.envelope("entitlement_inactive", "{\"period_end\":\"2026-11-06T02:00:00.000Z\"}")),
        ])
        let client = makeClient()
        let body = APICoding.encode(TranscriptionRequest(language: .auto))
        await #expect(throws: APIError.quotaExceeded(requiredSeconds: 3601, remainingSeconds: 12)) {
            try await client.send(Endpoints.startTranscription(meetingID: UUID(), idempotencyKey: UUID(), body: body))
        }
        await #expect(throws: APIError.entitlementInactive(periodEnd: APICoding.parseDate("2026-11-06T02:00:00.000Z"))) {
            try await client.send(Endpoints.startTranscription(meetingID: UUID(), idempotencyKey: UUID(), body: body))
        }
    }

    @Test func conflictCodes() async {
        let rev = "0b6d1f0e-1111-4000-8000-000000000009"
        StubURLProtocol.reset([
            StubResponse(status: 409, body: Self.envelope("revision_conflict", "{\"current_revision_number\":7,\"current_revision_id\":\"\(rev)\"}")),
            StubResponse(status: 409, body: Self.envelope("upload_offset_mismatch", "{\"received_bytes\":8388608}")),
            StubResponse(status: 409, body: Self.envelope("metadata_conflict", "{\"current_metadata_revision\":4}")),
            StubResponse(status: 409, body: Self.envelope("upload_not_open", "{\"status\":\"completed\"}")),
        ])
        let client = makeClient()
        let id = UUID()
        await #expect(throws: APIError.revisionConflict(currentRevisionNumber: 7, currentRevisionId: UUID(uuidString: rev))) {
            try await client.send(Endpoints.putDocument(meetingID: id, kind: .mom, expectedRevision: 6, baseRevision: 6, mutationID: UUID(), content: Data("{}".utf8)))
        }
        await #expect(throws: APIError.uploadOffsetMismatch(receivedBytes: 8_388_608)) { try await client.send(Endpoints.uploadChunk(id, offset: 0)) }
        await #expect(throws: APIError.metadataConflict(currentMetadataRevision: 4)) { try await client.send(Endpoints.patchMeeting(id, MeetingPatchRequest())) }
        await #expect(throws: APIError.uploadNotOpen(status: "completed")) { try await client.send(Endpoints.completeUpload(id)) }
    }

    @Test func goneValidationAndRateLimit() async {
        StubURLProtocol.reset([
            StubResponse(status: 410, body: Self.envelope("upload_expired")),
            StubResponse(status: 410, body: Self.envelope("audio_unavailable")),
            StubResponse(status: 422, body: Self.envelope("validation_failed", "{\"fields\":[{\"field\":\"folder_id\",\"reason\":\"not_found\"}]}")),
            StubResponse(status: 422, body: Self.envelope("invalid_content", "{\"path\":\"/sections/0/items/1/text\",\"reason\":\"required\"}")),
            StubResponse(status: 429, body: Self.envelope("ai_generation_limit", "{\"limit\":30,\"remaining\":0,\"next_slot_at\":\"2026-11-06T04:00:00.000Z\"}"), headers: ["Retry-After": "120"]),
        ])
        let client = makeClient()
        let id = UUID()
        await #expect(throws: APIError.uploadExpired) { try await client.send(Endpoints.upload(id)) }
        await #expect(throws: APIError.audioUnavailable) { try await client.send(Endpoints.audioURL(meetingID: id)) }
        await #expect(throws: APIError.validationFailed([FieldError(field: "folder_id", reason: "not_found")])) {
            try await client.send(Endpoints.patchMeeting(id, MeetingPatchRequest(folderId: .set(UUID()))))
        }
        await #expect(throws: APIError.invalidContent(path: "/sections/0/items/1/text", reason: "required")) {
            try await client.send(Endpoints.putDocument(meetingID: id, kind: .aiNotes, expectedRevision: 1, baseRevision: 1, mutationID: UUID(), content: Data("{}".utf8)))
        }
        await #expect(throws: APIError.aiGenerationLimit(nextSlotAt: APICoding.parseDate("2026-11-06T04:00:00.000Z"))) {
            try await client.send(Endpoints.generateNotes(meetingID: id, idempotencyKey: UUID(), body: APICoding.encode(GenerateNotesRequest(language: nil))))
        }
        #expect(!unauthenticated.value)
    }

    @Test func ambiguousAndTransportErrors() async {
        StubURLProtocol.reset([
            StubResponse(status: 503, body: Self.envelope("unavailable")),
            StubResponse(status: 0, body: "", error: URLError(.timedOut)),
            StubResponse(status: 0, body: "", error: URLError(.notConnectedToInternet)),
            StubResponse(status: 502, body: "<html>"),
        ])
        let client = makeClient()
        for expected in [APIError.unavailable, .timeout, .offline, .server(status: 502, code: nil)] {
            do {
                _ = try await client.send(Endpoints.usage())
                Issue.record("expected \(expected)")
            } catch let error as APIError {
                #expect(error == expected)
                #expect(error.isAmbiguous == (expected != .offline))
            } catch {
                Issue.record("unexpected \(error)")
            }
        }
    }

    @Test func deleteNotFoundCountsAsSuccess() async throws {
        StubURLProtocol.reset([StubResponse(status: 404, body: Self.envelope("not_found"))])
        let result = try await makeClient().sendDelete(Endpoints.deleteMeeting(UUID()))
        #expect(result == nil)
    }

    // MARK: idempotency

    @Test func idempotencyKeyAndBodyIdenticalAcrossRetries() async throws {
        let meetingID = UUID()
        let intent = PendingIntent.transcribe(meetingID: meetingID, language: .id)
        #expect(Fixtures.string(intent.body) == "{\"language\":\"id\",\"notes\":\"auto\"}")
        StubURLProtocol.reset([
            StubResponse(status: 503, body: Self.envelope("unavailable")),
            StubResponse(status: 0, body: "", error: URLError(.networkConnectionLost)),
            StubResponse(status: 200, body: Self.jobJSON),
        ])
        let client = makeClient()
        var job: JobDTO?
        for _ in 0..<3 where job == nil {
            do {
                job = try await client.send(Endpoints.startTranscription(meetingID: meetingID, idempotencyKey: intent.idempotencyKey, body: intent.body)).job
            } catch let error as APIError {
                #expect(error.isAmbiguous)
            }
        }
        #expect(job?.status == .queued)
        let captured = StubURLProtocol.captured
        #expect(captured.count == 3)
        let keys = Set(captured.map { $0.request.value(forHTTPHeaderField: "Idempotency-Key") })
        #expect(keys == [intent.idempotencyKey.lower])
        #expect(Set(captured.map(\.body)).count == 1)
        #expect(captured.allSatisfy { $0.request.url?.path == "/v1/meetings/\(meetingID.lower)/transcription" })
    }

    @Test func putDocumentBodyIsStableForSameMutation() throws {
        let mutation = UUID()
        let content = try DocumentContentValue.manualNotes(ManualNotesContent(items: [NoteItem(id: "n1", text: "a", atMs: nil), NoteItem(id: "n2", text: " ", atMs: nil)])).wireData()
        let a = Endpoints.putDocument(meetingID: UUID(), kind: .manualNotes, expectedRevision: 3, baseRevision: 2, mutationID: mutation, content: content)
        let body = try #require(a.body)
        #expect(Fixtures.string(body) == "{\"base_revision\":2,\"content\":{\"items\":[{\"at_ms\":null,\"id\":\"n1\",\"text\":\"a\"}],\"schema_version\":1},\"expected_revision\":3,\"mutation_id\":\"\(mutation.lower)\"}")
        #expect((try JSONSerialization.jsonObject(with: body)) is [String: Any])
    }

    // MARK: explicit nulls and Patch

    @Test func picAndFolderNullOnTheWire() throws {
        let action = Fixtures.string(try APICoding.encoder().encode(ActionItem.human(description: "x")))
        #expect(action.contains("\"pic\":null"))

        let create = CreateMeetingRequest(id: UUID(), source: .recording, title: "", startedAt: nil, timezone: "Asia/Jakarta", language: .auto, participants: [], folderId: nil, tagIds: [])
        let createJSON = Fixtures.string(APICoding.encode(create))
        #expect(createJSON.contains("\"folder_id\":null"))
        #expect(createJSON.contains("\"started_at\":null"))
        #expect(createJSON.contains("\"id\":\"\(create.id.lower)\""))

        let clear = Fixtures.string(APICoding.encode(MeetingPatchRequest(folderId: .clear)))
        #expect(clear == "{\"folder_id\":null}")

        let gen = Fixtures.string(APICoding.encode(GenerateMoMRequest(aiNotesRevisionId: nil, language: nil)))
        #expect(gen == "{\"ai_notes_revision_id\":null,\"language\":null}")
    }

    @Test func patchUnchangedOmitsKey() {
        let tag = UUID()
        let patch = MeetingPatchRequest(expectedMetadataRevision: 3, title: .set("Weekly"), tagIds: .set([tag]))
        let json = Fixtures.string(APICoding.encode(patch))
        #expect(json == "{\"expected_metadata_revision\":3,\"tag_ids\":[\"\(tag.lower)\"],\"title\":\"Weekly\"}")
        #expect(patch.hasMetadataChanges)
        #expect(Fixtures.string(APICoding.encode(MeetingPatchRequest())) == "{}")
        #expect(MeetingPatchRequest(folderId: .set(tag)).hasMetadataChanges == false)
    }
}
